package server

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/audit"
	"omnora/internal/catalog"
	"omnora/internal/config"
	"omnora/internal/confirmation"
	"omnora/internal/domain"
	"omnora/internal/fileops"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
	"omnora/internal/membershare"
	"omnora/internal/ratelimit"
	"omnora/internal/store"
	"omnora/internal/transferticket"
)

type Server struct {
	cfg               config.Config
	db                *store.DB
	mux               *http.ServeMux
	startupErr        error
	httpPolicy        *HTTPPolicy
	httpTrustRequired bool
	guard             *access.Guard
	tokens            *aitoken.Service
	confirmations     *confirmation.Service
	transferTickets   *transferticket.Service
	auditRecorder     audit.Recorder
	authLimiter       *ratelimit.Limiter
	memberFiles       *memberfiles.Service
	memberShares      *membershare.Service
	routesMu          sync.RWMutex
	listeners         *ListenerManager
	shutdownOnce      sync.Once
	shutdownCh        chan struct{}
}

const EntryHTTP = "http"

type Option func(*Server)

func WithListeners(listeners *ListenerManager) Option {
	return func(s *Server) {
		s.listeners = listeners
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
		cfg:        cfg,
		db:         db,
		mux:        http.NewServeMux(),
		shutdownCh: make(chan struct{}),
	}
	if db != nil {
		s.guard = access.NewGuard(db.SQL())
		s.tokens = aitoken.NewService(db.SQL())
		s.confirmations = confirmation.NewService(db.SQL())
		s.transferTickets = transferticket.NewService(db.SQL(), s.tokens, s.guard)
		s.auditRecorder = audit.NewRecorder(db.SQL())
		s.memberShares = membershare.NewService(db.SQL(), s.guard, membershare.WithAITokenService(s.tokens))
		fileOpsCoordinator := fileops.NewCoordinator(db.SQL(), fileops.WithShareInvalidator(s.memberShares))
		s.memberFiles = memberfiles.NewService(db.SQL(), s.guard, catalog.NewService(db.SQL()), memberfiles.WithAITokenService(s.tokens), memberfiles.WithShareInvalidator(s.memberShares), memberfiles.WithFileOpsCoordinator(fileOpsCoordinator))
	}
	s.authLimiter = ratelimit.New(ratelimit.Options{})
	for _, opt := range opts {
		opt(s)
	}
	if policy, err := newHTTPPolicy(cfg.HTTP); err != nil {
		s.startupErr = err
	} else {
		s.httpPolicy = policy
	}
	if db != nil {
		if err := s.hydrateRouteGroups(context.Background()); err != nil {
			if s.startupErr == nil {
				s.startupErr = err
			}
		}
	}
	s.routes()
	return s
}

// StartupError reports a fail-closed initialization error that must prevent
// workers and listeners from starting. New keeps the historical handler API;
// the executable checks this gate before binding HTTP.
func (s *Server) StartupError() error {
	return s.startupErr
}

// RequireHTTPTrustBoundary asks the server to fail closed on business routes
// when OMNORA_PUBLIC_URL (or an explicit host/origin allowlist) is unset. Unit
// tests leave this unset so handlers stay callable without a public-origin
// fixture; cmd/omnora enables it for real deployments.
func (s *Server) RequireHTTPTrustBoundary() {
	if s == nil {
		return
	}
	s.httpTrustRequired = true
}

// MarkReady persists the final normal-process readiness gate after all
// migration, identity-rollout, and route-hydration checks have passed.
// Missing PublicURL is not fatal here: /readyz reports public_url_required
// until the operator sets the post-deploy origin.
func (s *Server) MarkReady(ctx context.Context) error {
	if s.startupErr != nil {
		return s.startupErr
	}
	if s.businessRoutesExposed() {
		if err := s.cfg.ValidateBusinessExposure(); err != nil && !errors.Is(err, config.ErrPublicURLRequired) {
			return err
		}
		if err := s.cfg.ValidateAuditHMACKey(); err != nil {
			return err
		}
	}
	if s.db == nil {
		return nil
	}
	for _, key := range []string{"mcp_audit_risk", "audit_write_risk"} {
		var risk string
		err := s.db.SQL().QueryRowContext(ctx, `SELECT value FROM system_state WHERE key = ?`, key).Scan(&risk)
		if err == nil {
			return errors.New("audit readiness risk is present")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	result, err := s.db.SQL().ExecContext(ctx, `
UPDATE recovery_control
SET ready = 1, updated_at = CURRENT_TIMESTAMP
WHERE id = 1 AND state = 'normal'
`)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("recovery control is not in normal state")
	}
	return nil
}

// RequestShutdown asks the process entry point to begin a controlled exit.
// A durable restore request must stop this process from continuing to serve
// business routes against a database that an offline recovery worker is
// about to replace wholesale; simply marking readiness false is not enough
// because business routes are not otherwise gated on recovery state. Safe to
// call multiple times or concurrently; only the first call has an effect.
func (s *Server) RequestShutdown() {
	if s == nil {
		return
	}
	s.shutdownOnce.Do(func() {
		close(s.shutdownCh)
	})
}

// ShutdownRequested returns a channel that closes once RequestShutdown has
// been called. The process entry point selects on it alongside OS signals so
// it can drain listeners and exit the same way it would for SIGTERM.
func (s *Server) ShutdownRequested() <-chan struct{} {
	return s.shutdownCh
}

func (s *Server) businessRoutesExposed() bool {
	for _, group := range domain.AllRouteGroups {
		switch group {
		case domain.RouteGroupMemberWeb, domain.RouteGroupAdminWeb, domain.RouteGroupShare,
			domain.RouteGroupREST, domain.RouteGroupMCP, domain.RouteGroupOpenAPI:
			if s.routeEnabled(group) {
				return true
			}
		}
	}
	return false
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return securityHeaders(requestID(s.trustBoundary(accessLog(recoverPanic(s.mux)))))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)

	s.apiRoutes()

	s.handleWebGroup(domain.RouteGroupMemberWeb, "/app")
	s.handleWebGroup(domain.RouteGroupAdminWeb, "/admin")
	s.handleWebGroup(domain.RouteGroupShare, "/share")
	s.handleProductGroup(domain.RouteGroupREST, "/api/v1")
	s.mcpRoutes()
	s.handleProductGroup(domain.RouteGroupOpenAPI, "/openapi")
	s.mux.Handle("/", s.memberRoot())
}

func (s *Server) memberRoot() http.Handler {
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isStaticAssetRequest(r.URL.Path) {
			s.static(w, r)
			return
		}
		s.serveGroupSPA(w, r)
	})
	return s.gate(domain.RouteGroupMemberWeb, spa)
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
		s.routeSecurity(routeRuleFor(r), next).ServeHTTP(w, r)
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
	if s.httpTrustRequired && s.businessRoutesExposed() {
		if err := s.cfg.ValidateBusinessExposure(); err != nil {
			if errors.Is(err, config.ErrPublicURLRequired) {
				httpx.WriteError(w, r, http.StatusServiceUnavailable, "public_url_required", "OMNORA_PUBLIC_URL must be configured before serving business routes")
				return
			}
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "invalid_http_trust", err.Error())
			return
		}
	}
	if s.db != nil {
		if err := s.db.Ping(r.Context()); err != nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is not ready")
			return
		}
		var risk string
		err := s.db.SQL().QueryRowContext(r.Context(), `SELECT value FROM system_state WHERE key = 'mcp_audit_risk'`).Scan(&risk)
		if err == nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "mcp_audit_degraded", "mcp_audit_degraded: MCP audit integrity requires investigation")
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is not ready")
			return
		}
		if err := s.db.SQL().QueryRowContext(r.Context(), `SELECT value FROM system_state WHERE key = 'audit_write_risk'`).Scan(&risk); err == nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "audit_degraded", "audit integrity requires investigation")
			return
		} else if !errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is not ready")
			return
		}
		var recoveryState string
		var recoveryReady int
		if err := s.db.SQL().QueryRowContext(r.Context(), `
SELECT state, ready FROM recovery_control WHERE id = 1
`).Scan(&recoveryState, &recoveryReady); err != nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "recovery_unavailable", "recovery control is not ready")
			return
		}
		if recoveryState != "normal" || recoveryReady != 1 {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "recovery_not_ready", "recovery control is not ready")
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
		// Propagate the generated ID to protocol adapters through a cloned
		// request header as well as context, without mutating the caller-owned
		// request. MCP tool errors can then include the same stable ID.
		cloned := r.Clone(httpx.WithRequestID(r.Context(), requestID))
		cloned.Header = r.Header.Clone()
		cloned.Header.Set("X-Request-ID", requestID)
		next.ServeHTTP(w, cloned)
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
			slog.String("client_ip", clientIPFromRequest(r)),
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
