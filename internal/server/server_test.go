package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/store"
)

func TestNewServerRecordsRouteHydrationFailure(t *testing.T) {
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "closed.db"), BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	srv := NewServer(config.Config{Routes: map[domain.RouteGroup]bool{}}, db)
	if srv.StartupError() == nil {
		t.Fatal("NewServer() did not record route hydration failure")
	}
}

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

func TestRequestLoggingPreservesUpstreamRequestID(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/", nil)
	req.Header.Set("X-Request-ID", "proxy-req-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") != "proxy-req-123" {
		t.Fatalf("X-Request-ID = %q, want upstream id", rec.Header().Get("X-Request-ID"))
	}
	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.RequestID != "proxy-req-123" {
		t.Fatalf("response request_id = %q, want upstream id", body.Error.RequestID)
	}

	output := logs.String()
	for _, expected := range []string{
		`"msg":"http error response"`,
		`"msg":"http request completed"`,
		`"request_id":"proxy-req-123"`,
		`"status":404`,
		`"error_code":"route_group_disabled"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log output missing %s: %s", expected, output)
		}
	}
}

func TestRequestIDPropagatesGeneratedHeaderToMCPAdapters(t *testing.T) {
	var gotHeader, gotContext string
	handler := requestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Request-ID")
		gotContext = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if gotHeader == "" || gotHeader != gotContext || rec.Header().Get("X-Request-ID") != gotHeader {
		t.Fatalf("request ID propagation header=%q context=%q response=%q", gotHeader, gotContext, rec.Header().Get("X-Request-ID"))
	}
	if req.Header.Get("X-Request-ID") != "" {
		t.Fatalf("requestID mutated caller request: %q", req.Header.Get("X-Request-ID"))
	}
}

func TestPanicRecoveryLogsAndReturnsStableError(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	handler := securityHeaders(requestID(accessLog(recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))))
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set("X-Request-ID", "panic-req")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") != "panic-req" {
		t.Fatalf("X-Request-ID = %q, want panic-req", rec.Header().Get("X-Request-ID"))
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "internal_error" || body.Error.RequestID != "panic-req" {
		t.Fatalf("error body = %#v", body.Error)
	}

	output := logs.String()
	for _, expected := range []string{
		`"msg":"http request panic"`,
		`"msg":"http request completed"`,
		`"request_id":"panic-req"`,
		`"status":500`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("panic log output missing %s: %s", expected, output)
		}
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
		group      domain.RouteGroup
		path       string
		wantStatus int
		wantCode   string
	}{
		{group: domain.RouteGroupREST, path: "/api/v1/unknown", wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{group: domain.RouteGroupMCP, path: "/mcp/unknown", wantStatus: http.StatusNotFound},
		{group: domain.RouteGroupOpenAPI, path: "/openapi/unknown", wantStatus: http.StatusNotImplemented, wantCode: "not_implemented"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			handler := New(config.Config{Routes: map[domain.RouteGroup]bool{tc.group: true}}, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.group == domain.RouteGroupMCP {
				return
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
			if body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %s", body.Error.Code, tc.wantCode)
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
	skipLegacySpaceRESTTest(t)
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

func TestStaticAssetsRequireMemberWebRouteGroup(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/placeholder.txt", nil)
	handler.ServeHTTP(rec, req)

	assertRouteGroupDisabled(t, rec)
}

func TestUnknownStaticExtensionReturnsNotFound(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	handler.ServeHTTP(rec, req)

	assertRouteGroupDisabled(t, rec)
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
