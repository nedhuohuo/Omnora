package server

import (
	"embed"
	"net/http"

	"omnora/internal/domain"
)

//go:embed openapi_assets/*.yaml
var openAPIAssets embed.FS

const openAPISpecPath = "openapi_assets/omnora.v1.yaml"

func (s *Server) registerOpenAPIRoutes() {
	handler := s.gate(domain.RouteGroupOpenAPI, http.HandlerFunc(s.serveOpenAPISpec))
	s.mux.Handle("GET /openapi", handler)
	s.mux.Handle("GET /openapi/omnora.v1.yaml", handler)
}

func (s *Server) serveOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	body, err := openAPIAssets.ReadFile(openAPISpecPath)
	if err != nil {
		http.Error(w, "OpenAPI specification is unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
