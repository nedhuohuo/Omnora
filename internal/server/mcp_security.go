package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"omnora/internal/domain"
	"omnora/internal/httpx"
)

func (s *Server) mcpSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.MCP.AllowedHosts) == 0 {
			httpx.WriteError(w, r, http.StatusForbidden, "mcp_configuration_error", "MCP host allowlist is not configured")
			return
		}
		if !mcpHostAllowed(r.Host, s.cfg.MCP.AllowedHosts) {
			httpx.WriteError(w, r, http.StatusForbidden, "mcp_host_forbidden", "MCP host is not allowed")
			return
		}
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && !mcpOriginAllowed(origin, s.cfg.MCP.AllowedOrigins) {
			httpx.WriteError(w, r, http.StatusForbidden, "mcp_origin_forbidden", "MCP origin is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mcpHostAllowed(requestHost string, allowed []string) bool {
	requestHost = normalizeMCPHost(requestHost)
	if requestHost == "" {
		return false
	}
	for _, candidate := range allowed {
		if normalized := normalizeMCPHost(candidate); normalized != "" && normalized == requestHost {
			return true
		}
	}
	return false
}

func mcpOriginAllowed(origin string, allowed []string) bool {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	actual := strings.ToLower(parsed.Scheme) + "://" + normalizeMCPHost(parsed.Host)
	if actual == "://" {
		return false
	}
	for _, candidate := range allowed {
		parsedCandidate, err := url.Parse(strings.TrimSpace(candidate))
		if err != nil || parsedCandidate.User != nil || parsedCandidate.Host == "" || parsedCandidate.Path != "" && parsedCandidate.Path != "/" || parsedCandidate.RawQuery != "" || parsedCandidate.Fragment != "" {
			continue
		}
		candidateOrigin := strings.ToLower(parsedCandidate.Scheme) + "://" + normalizeMCPHost(parsedCandidate.Host)
		if (parsedCandidate.Scheme == "http" || parsedCandidate.Scheme == "https") && candidateOrigin == actual {
			return true
		}
	}
	return false
}

func normalizeMCPHost(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(value), "."))
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return ""
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		if host == "" || port == "" {
			return ""
		}
		return net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	if strings.Contains(value, ":") {
		// Bare IPv6 literals are ambiguous in Host and must be bracketed.
		if net.ParseIP(value) != nil {
			return "[" + value + "]"
		}
		return ""
	}
	return value
}

func (s *Server) mcpRoutes() {
	standard := s.gate(domain.RouteGroupMCP, s.mcpSecurity(newMCPAPIHandler(s)))
	s.mux.Handle("/mcp", standard)
	transfers := s.gate(domain.RouteGroupMCP, s.mcpSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			s.mcpDownload(w, r)
			return
		}
		if r.Method == http.MethodPut {
			s.mcpUploadPart(w, r)
			return
		}
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		w.WriteHeader(http.StatusMethodNotAllowed)
	})))
	s.mux.Handle("GET /mcp/transfers/{publicId}", transfers)
	s.mux.Handle("PUT /mcp/transfers/{publicId}/parts/{partNumber}", transfers)
	// Reserve the remainder of the subtree so unrelated routes cannot expose
	// ticket records or host filesystem paths.
	s.mux.Handle("/mcp/", s.gate(domain.RouteGroupMCP, http.NotFoundHandler()))
}
