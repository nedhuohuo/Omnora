package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
)

func TestRouteGroupsFailClosed(t *testing.T) {
	handler := New(config.Config{
		Routes: map[domain.RouteGroup]bool{},
	}, nil)

	for _, path := range []string{"/app/", "/admin/", "/share/", "/api/v1/", "/mcp/", "/openapi/"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}

			var body struct {
				Error struct {
					Code      string `json:"code"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != "route_group_disabled" {
				t.Fatalf("error code = %q", body.Error.Code)
			}
			if body.Error.RequestID == "" {
				t.Fatal("request_id should be present")
			}
		})
	}
}

func TestMemberWebEntriesRequireMemberRouteGroup(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)

	for _, target := range []string{"/", "/app", "/app/", "/index.html"} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

			assertRouteGroupDisabled(t, rec)
		})
	}
}

func TestMemberWebEntriesServeSPAWhenEnabled(t *testing.T) {
	handler := New(config.Config{
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupMemberWeb: true,
		},
	}, nil)

	for _, target := range []string{"/", "/app", "/app/", "/console/mounts"} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

			assertEmbeddedSPA(t, rec)
		})
	}
}

func TestAdminAndShareEntriesRequireTheirRouteGroups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group domain.RouteGroup
		paths []string
	}{
		{name: "admin", group: domain.RouteGroupAdminWeb, paths: []string{"/admin", "/admin/", "/admin/mounts"}},
		{name: "share", group: domain.RouteGroupShare, paths: []string{"/share", "/share/", "/share/item"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)
			for _, target := range tc.paths {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
				assertRouteGroupDisabled(t, rec)
			}
		})
	}
}

func TestAdminAndShareEntriesServeSPAWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group domain.RouteGroup
		path  string
	}{
		{name: "admin exact", group: domain.RouteGroupAdminWeb, path: "/admin"},
		{name: "admin slash", group: domain.RouteGroupAdminWeb, path: "/admin/"},
		{name: "share exact", group: domain.RouteGroupShare, path: "/share"},
		{name: "share slash", group: domain.RouteGroupShare, path: "/share/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(config.Config{
				Routes: map[domain.RouteGroup]bool{tc.group: true},
			}, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			assertEmbeddedSPA(t, rec)
		})
	}
}

func TestEnabledProductGroupFallbacksStayJSONErrors(t *testing.T) {
	for _, tc := range []struct {
		group domain.RouteGroup
		path  string
	}{
		{group: domain.RouteGroupREST, path: "/api/v1/unknown"},
		{group: domain.RouteGroupMCP, path: "/mcp/unknown"},
		{group: domain.RouteGroupOpenAPI, path: "/openapi/unknown"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			handler := New(config.Config{Routes: map[domain.RouteGroup]bool{tc.group: true}}, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type = %q, want JSON", rec.Header().Get("Content-Type"))
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != "not_implemented" {
				t.Fatalf("error code = %q, want not_implemented", body.Error.Code)
			}
			if strings.Contains(rec.Body.String(), `<div id="root">`) {
				t.Fatal("product fallback must not return the SPA")
			}
		})
	}
}

func TestWebGroupUnknownAssetsReturnNotFound(t *testing.T) {
	for _, tc := range []struct {
		group domain.RouteGroup
		path  string
	}{
		{group: domain.RouteGroupMemberWeb, path: "/app/missing.js"},
		{group: domain.RouteGroupAdminWeb, path: "/admin/missing.js"},
		{group: domain.RouteGroupShare, path: "/share/missing.js"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			handler := New(config.Config{Routes: map[domain.RouteGroup]bool{tc.group: true}}, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			if strings.Contains(rec.Body.String(), `<div id="root">`) {
				t.Fatal("unknown asset must not return the SPA")
			}
		})
	}
}

func TestEnabledRESTRouteRequiresSessionForSpaces(t *testing.T) {
	routes := map[domain.RouteGroup]bool{
		domain.RouteGroupREST: true,
	}
	handler := New(config.Config{Routes: routes}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestBootstrapReturnsRouteGroupsWhenRESTEnabled(t *testing.T) {
	routes := map[domain.RouteGroup]bool{
		domain.RouteGroupREST: true,
	}
	handler := New(config.Config{Routes: routes}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		RouteGroups []struct {
			ID      string `json:"id"`
			Exposed bool   `json:"exposed"`
		} `json:"routeGroups"`
		AdminRisks []struct {
			Label string `json:"label"`
		} `json:"adminRisks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if len(body.RouteGroups) != len(domain.AllRouteGroups) {
		t.Fatalf("routeGroups len = %d, want %d", len(body.RouteGroups), len(domain.AllRouteGroups))
	}
	if len(body.AdminRisks) == 0 {
		t.Fatal("adminRisks should describe missing database")
	}
}

func TestHealthBypassesRouteGroups(t *testing.T) {
	handler := New(config.Config{
		Routes: map[domain.RouteGroup]bool{},
	}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestStaticAssetsBypassMemberWebRouteGroup(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/placeholder.txt", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Content-Type") == "" {
		t.Fatal("content type should be set")
	}
}

func TestUnknownStaticExtensionReturnsNotFound(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func assertRouteGroupDisabled(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "route_group_disabled" {
		t.Fatalf("error code = %q, want route_group_disabled", body.Error.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content type = %q, want JSON", rec.Header().Get("Content-Type"))
	}
}

func assertEmbeddedSPA(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Content-Type") == "" {
		t.Fatal("content type should be set")
	}
	if !strings.Contains(rec.Body.String(), `<div id="root">`) {
		t.Fatalf("response does not contain embedded SPA: %q", rec.Body.String())
	}
}
