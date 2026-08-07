package server

import (
	"net/http"
	"net/url"
	"testing"
)

func TestRouteMatrixDeclaresCredentialBoundaries(t *testing.T) {
	tests := []struct {
		method, path string
		mode         RouteAuthMode
		csrf         bool
		reauth       bool
	}{
		{http.MethodPost, "/api/v1/auth/session", RouteAuthPublic, false, false},
		{http.MethodPatch, "/api/v1/account/password", RouteAuthCookie, true, true},
		{http.MethodGet, "/api/v1/admin/overview", RouteAuthCookie, false, false},
		{http.MethodDelete, "/api/v1/admin/ai-tokens/tok_1", RouteAuthCookie, true, true},
		{http.MethodGet, "/api/v1/share/current", RouteAuthShareCookie, false, false},
		{http.MethodPost, "/api/v1/shares", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/share/action", RouteAuthShareCookie, true, false},
		{http.MethodPost, "/mcp", RouteAuthBearer, false, false},
	}
	for _, tt := range tests {
		rule := routeRuleFor(httpRequest(tt.method, tt.path))
		if rule.Mode != tt.mode || rule.CSRF != tt.csrf || rule.RequiresRecentAuth != tt.reauth {
			t.Errorf("%s %s => %#v, want mode=%s csrf=%t reauth=%t", tt.method, tt.path, rule, tt.mode, tt.csrf, tt.reauth)
		}
	}
}

func httpRequest(method, path string) *http.Request {
	return &http.Request{Method: method, URL: mustURL(path)}
}

func mustURL(path string) *url.URL {
	parsed, err := url.Parse(path)
	if err != nil {
		panic(err)
	}
	return parsed
}
