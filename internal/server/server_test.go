package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
