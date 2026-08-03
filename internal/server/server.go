package server

import (
	"net/http"
	"strings"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/store"
)

type Server struct {
	cfg config.Config
	db  *store.DB
	mux *http.ServeMux
}

func New(cfg config.Config, db *store.DB) http.Handler {
	s := &Server{
		cfg: cfg,
		db:  db,
		mux: http.NewServeMux(),
	}
	s.routes()
	return securityHeaders(requestID(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)

	s.apiRoutes()

	s.handleGroup(domain.RouteGroupMemberWeb, "/app/")
	s.handleGroup(domain.RouteGroupAdminWeb, "/admin/")
	s.handleGroup(domain.RouteGroupShare, "/share/")
	s.handleGroup(domain.RouteGroupREST, "/api/v1/")
	s.handleGroup(domain.RouteGroupMCP, "/mcp/")
	s.handleGroup(domain.RouteGroupOpenAPI, "/openapi/")
}

func (s *Server) handleGroup(group domain.RouteGroup, prefix string) {
	handler := s.gate(group, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotImplemented, "not_implemented", "route group is enabled but no product handler is implemented yet")
	}))

	exact := strings.TrimSuffix(prefix, "/")
	s.mux.Handle(exact, handler)
	s.mux.Handle(prefix, handler)
}

func (s *Server) gate(group domain.RouteGroup, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Routes.Enabled(group) {
			httpx.WriteError(w, r, http.StatusNotFound, "route_group_disabled", "route group is not exposed")
			return
		}
		next.ServeHTTP(w, r)
	})
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
