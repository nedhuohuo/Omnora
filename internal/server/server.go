package server

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/store"
)

type Server struct {
	cfg            config.Config
	db             *store.DB
	mux            *http.ServeMux
	routesMu       sync.RWMutex
	mountDiscovery mountDiscoveryFunc
	listeners      *ListenerManager
}

const EntryHTTP = "http"

var mountRegistrationMu sync.Mutex

type Option func(*Server)

func WithListeners(listeners *ListenerManager) Option {
	return func(s *Server) {
		s.listeners = listeners
	}
}

func withMountDiscovery(discovery mountDiscoveryFunc) Option {
	return func(s *Server) {
		s.mountDiscovery = discovery
	}
}

// The Docker build replaces the placeholder with the Vite production bundle.
// Keeping the bundle in the Go binary makes the test deployment one service.
//
//go:embed static/*
var staticFiles embed.FS

// New builds a server and returns its HTTP handler.
func New(cfg config.Config, db *store.DB, opts ...Option) http.Handler {
	return NewServer(cfg, db, opts...).Handler()
}

// NewServer constructs the Server, hydrates route state from the database, and
// registers routes.
func NewServer(cfg config.Config, db *store.DB, opts ...Option) *Server {
	s := &Server{
		cfg:            cfg,
		db:             db,
		mux:            http.NewServeMux(),
		mountDiscovery: discoverConfiguredMounts,
	}
	for _, opt := range opts {
		opt(s)
	}
	if db != nil {
		ctx := context.Background()
		_ = s.hydrateRouteGroups(ctx)
		if registered, err := s.autoRegisterDockerMounts(ctx, "", ""); err != nil {
			slog.Warn("automatic Docker mount registration incomplete", "registered", registered, "error", err)
		} else if registered > 0 {
			slog.Info("automatic Docker mount registration completed", "registered", registered)
		}
	}
	s.routes()
	return s
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return securityHeaders(requestID(accessLog(recoverPanic(s.mux))))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)

	s.apiRoutes()

	s.handleWebGroup(domain.RouteGroupMemberWeb, "/app")
	s.handleWebGroup(domain.RouteGroupAdminWeb, "/admin")
	s.handleWebGroup(domain.RouteGroupShare, "/share")
	s.handleProductGroup(domain.RouteGroupREST, "/api/v1")
	s.handleProductGroup(domain.RouteGroupMCP, "/mcp")
	s.handleProductGroup(domain.RouteGroupOpenAPI, "/openapi")
	s.mux.Handle("/", s.memberRoot())
}

func (s *Server) memberRoot() http.Handler {
	spa := s.gate(domain.RouteGroupMemberWeb, http.HandlerFunc(s.serveGroupSPA))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isStaticAssetRequest(r.URL.Path) {
			s.static(w, r)
			return
		}
		spa.ServeHTTP(w, r)
	})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requested := s.staticPath(r.URL.Path)
	root, err := s.staticRoot()
	if err != nil {
		http.Error(w, "static files are unavailable", http.StatusInternalServerError)
		return
	}
	if _, err := fs.Stat(root, requested); err != nil {
		http.NotFound(w, r)
		return
	}
	s.serveStaticPath(w, r, root, requested)
}

func (s *Server) serveGroupSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if path.Ext(s.staticPath(r.URL.Path)) != "" {
		s.static(w, r)
		return
	}

	root, err := s.staticRoot()
	if err != nil {
		http.Error(w, "static files are unavailable", http.StatusInternalServerError)
		return
	}
	s.serveStaticPath(w, r, root, "index.html")
}

func (s *Server) isStaticAssetRequest(requestPath string) bool {
	requested := s.staticPath(requestPath)
	if requested == "" || requested == "." || requested == "index.html" {
		return false
	}
	return path.Ext(requested) != ""
}

func (s *Server) staticPath(requestPath string) string {
	return strings.TrimPrefix(path.Clean("/"+requestPath), "/")
}

func (s *Server) staticRoot() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}

func (s *Server) serveStaticPath(w http.ResponseWriter, r *http.Request, root fs.FS, requested string) {
	http.ServeFileFS(w, r, root, requested)
}

func (s *Server) handleWebGroup(group domain.RouteGroup, routePath string) {
	handler := s.gate(group, http.HandlerFunc(s.serveGroupSPA))
	s.mux.Handle(routePath, handler)
	s.mux.Handle(routePath+"/", handler)
}

func (s *Server) handleProductGroup(group domain.RouteGroup, routePath string) {
	handler := s.gate(group, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotImplemented, "not_implemented", "route group is enabled but no product handler is implemented yet")
	}))
	s.mux.Handle(routePath, handler)
	s.mux.Handle(routePath+"/", handler)
}

func (s *Server) gate(group domain.RouteGroup, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.routeEnabled(group) {
			httpx.WriteError(w, r, http.StatusNotFound, "route_group_disabled", "route group is not exposed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routeEnabled(group domain.RouteGroup) bool {
	s.routesMu.RLock()
	defer s.routesMu.RUnlock()
	return s.cfg.Routes.Enabled(group)
}

func (s *Server) setRouteEnabled(group domain.RouteGroup, enabled bool) {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	s.cfg.Routes.Set(group, enabled)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.db != nil {
		if err := s.db.Ping(r.Context()); err != nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is not ready")
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := httpx.RequestIDFromHeader(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = httpx.NewRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), requestID)))
	})
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		duration := time.Since(start)
		level := slog.LevelInfo
		if recorder.status >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if recorder.status >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		slog.LogAttrs(r.Context(), level, "http request completed",
			slog.String("request_id", httpx.RequestID(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Int64("duration_ms", duration.Milliseconds()),
			slog.Int64("bytes", recorder.bytes),
			slog.String("remote_addr", r.RemoteAddr),
			slog.String("user_agent", r.UserAgent()),
		)
	})
}

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(r.Context(), "http request panic",
					"request_id", httpx.RequestID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", recovered,
				)
				httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "request could not be completed")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(w.status)
	}
	n, err := w.ResponseWriter.Write(payload)
	w.bytes += int64(n)
	return n, err
}

func (w *loggingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}
