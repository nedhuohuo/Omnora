package server

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/store"
)

type Server struct {
	cfg       config.Config
	db        *store.DB
	mux       *http.ServeMux
	routesMu  sync.RWMutex
	listeners *ListenerManager
	network   networkPolicyStore
}

const (
	// EntryLAN and EntryProxy identify the two network listeners a request
	// can arrive on. Sessions are bound to one of these.
	EntryLAN   = "lan_http"
	EntryProxy = "proxy_https"
)

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

// New builds a server and returns the default (lan_http) entry handler.
func New(cfg config.Config, db *store.DB, opts ...Option) http.Handler {
	return NewServer(cfg, db, opts...).HandlerFor(EntryLAN)
}

// NewServer constructs the Server, hydrates policy from the database, and
// registers routes. Entry-specific handlers are built with HandlerFor.
func NewServer(cfg config.Config, db *store.DB, opts ...Option) *Server {
	s := &Server{
		cfg: cfg,
		db:  db,
		mux: http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	if db != nil {
		_ = s.hydrateRouteGroups(context.Background())
		s.hydrateNetworkPolicy(context.Background())
	}
	s.routes()
	return s
}

// HandlerFor returns a fully wrapped handler that tags every request with its
// network entry and applies that entry's gate.
func (s *Server) HandlerFor(entry string) http.Handler {
	return securityHeaders(requestID(s.entryGate(entry, s.mux)))
}

// entryGate tags the request with the network entry it arrived on, exempts
// health probes, then applies the entry's access gate.
func (s *Server) entryGate(entry string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(httpx.WithEntry(r.Context(), entry))
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		var allowed bool
		switch entry {
		case EntryLAN:
			allowed = s.lanGate(w, r)
		case EntryProxy:
			allowed = s.proxyGate(w, r)
		default:
			httpx.WriteError(w, r, http.StatusForbidden, "unknown_network_entry", "unknown network entry")
			return
		}
		if !allowed {
			return
		}
		next.ServeHTTP(w, r)
	})
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
		requestID := httpx.NewRequestID()
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), requestID)))
	})
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
