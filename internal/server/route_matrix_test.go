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
		{http.MethodPost, "/api/v1/account/totp/disable", RouteAuthCookie, true, true},
		{http.MethodDelete, "/api/v1/account/sessions/sess_1", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/ai-tokens", RouteAuthCookie, true, true},
		{http.MethodPatch, "/api/v1/ai-tokens/tok_1", RouteAuthCookie, true, true},
		{http.MethodDelete, "/api/v1/ai-tokens/tok_1", RouteAuthCookie, true, false},
		{http.MethodGet, "/api/v1/admin/overview", RouteAuthCookie, false, false},
		{http.MethodPatch, "/api/v1/admin/route-groups/mcp", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/admin/mounts", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/admin/index-jobs", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/admin/users", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/admin/users/u1/enable", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/admin/users/u1/disable", RouteAuthCookie, true, true},
		{http.MethodPost, "/api/v1/admin/users/u1/revoke-sessions", RouteAuthCookie, true, true},
		{http.MethodDelete, "/api/v1/admin/spaces/sp1", RouteAuthCookie, true, true},
		{http.MethodDelete, "/api/v1/admin/spaces/sp1/members/u1", RouteAuthCookie, true, false},
		{http.MethodDelete, "/api/v1/admin/ai-tokens/tok_1", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/admin/backups", RouteAuthCookie, true, true},
		{http.MethodPost, "/api/v1/admin/backups/b1/restore", RouteAuthCookie, true, true},
		{http.MethodGet, "/api/v1/share/current", RouteAuthShareCookie, false, false},
		{http.MethodPost, "/api/v1/shares", RouteAuthCookie, true, false},
		{http.MethodPost, "/api/v1/share/action", RouteAuthShareCookie, true, false},
		{http.MethodPost, "/api/v1/team-spaces", RouteAuthCookie, true, false},
		{http.MethodGet, "/api/v1/team-spaces/sp1/members", RouteAuthCookie, false, false},
		{http.MethodPut, "/api/v1/team-spaces/sp1/members/u1", RouteAuthCookie, true, false},
		{http.MethodDelete, "/api/v1/team-spaces/sp1/members/u1", RouteAuthCookie, true, false},
		{http.MethodPost, "/mcp", RouteAuthBearer, false, false},
	}
	for _, tt := range tests {
		rule := routeRuleFor(httpRequest(tt.method, tt.path))
		if rule.Mode != tt.mode || rule.CSRF != tt.csrf || rule.RequiresRecentAuth != tt.reauth {
			t.Errorf("%s %s => %#v, want mode=%s csrf=%t reauth=%t", tt.method, tt.path, rule, tt.mode, tt.csrf, tt.reauth)
		}
	}
}

func TestTeamSpaceRoutesAreExplicitlyDeclared(t *testing.T) {
	want := []RouteRule{
		{Method: http.MethodPost, Pattern: "/api/v1/team-spaces", Mode: RouteAuthCookie, CSRF: true},
		{Method: http.MethodGet, Pattern: "/api/v1/team-spaces/*/members", Mode: RouteAuthCookie},
		{Method: http.MethodPut, Pattern: "/api/v1/team-spaces/*/members/*", Mode: RouteAuthCookie, CSRF: true},
		{Method: http.MethodDelete, Pattern: "/api/v1/team-spaces/*/members/*", Mode: RouteAuthCookie, CSRF: true},
	}
	for _, expected := range want {
		found := false
		for _, rule := range routeMatrix {
			if rule.Method == expected.Method && rule.Pattern == expected.Pattern && rule.Mode == expected.Mode && rule.CSRF == expected.CSRF {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing explicit route rule %#v", expected)
		}
	}
}

func TestTeamSpaceMemberRoutePatternMatches(t *testing.T) {
	if !routePatternMatches("/api/v1/team-spaces/*/members/*", "/api/v1/team-spaces/sp1/members/u1") {
		t.Fatal("expected team-space member route to match")
	}
	if routePatternMatches("/api/v1/team-spaces/*/members/*", "/api/v1/team-spaces/sp1/members") {
		t.Fatal("member collection must not match member mutation rule")
	}
}

func TestRoutePatternMatchesMultiWildcard(t *testing.T) {
	if !routePatternMatches("/api/v1/admin/spaces/*/members/*", "/api/v1/admin/spaces/sp1/members/u1") {
		t.Fatal("expected multi-wildcard member path to match")
	}
	if routePatternMatches("/api/v1/admin/spaces/*/members/*", "/api/v1/admin/spaces/sp1") {
		t.Fatal("space root must not match member pattern")
	}
	if !routePatternMatches("/api/v1/admin/backups/*/restore", "/api/v1/admin/backups/b1/restore") {
		t.Fatal("expected backup restore pattern to match")
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
