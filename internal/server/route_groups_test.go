package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/store"
)

func TestAdminRouteGroupsListAndUpdate(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)

	memberRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/route-groups", memberCookie)
	if memberRec.Code != http.StatusForbidden {
		t.Fatalf("member list status = %d, want 403", memberRec.Code)
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/route-groups", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("admin list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listBody struct {
		Items []struct {
			ID      string `json:"id"`
			Exposed bool   `json:"exposed"`
			Label   string `json:"label"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listBody.Items) != len(domain.AllRouteGroups) {
		t.Fatalf("items = %d, want %d", len(listBody.Items), len(domain.AllRouteGroups))
	}
	for _, item := range listBody.Items {
		if item.ID == string(domain.RouteGroupMCP) {
			if item.Exposed {
				t.Fatal("mcp should start disabled in API test server")
			}
			if item.Label != "MCP" {
				t.Fatalf("mcp label = %q", item.Label)
			}
		}
	}

	body, err := json.Marshal(map[string]bool{"exposed": true})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/route-groups/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	updateRec := httptest.NewRecorder()
	handler.ServeHTTP(updateRec, req)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}
	var updated struct {
		ID      string `json:"id"`
		Exposed bool   `json:"exposed"`
	}
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.ID != "mcp" || !updated.Exposed {
		t.Fatalf("updated = %#v", updated)
	}

	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"method":"tools/list"}`)))
	mcpReq.Header.Set("Content-Type", "application/json")
	mcpRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpRec, mcpReq)
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(mcpRec.Body.Bytes(), &errBody)
	if errBody.Error.Code == "route_group_disabled" {
		t.Fatal("mcp remains route_group_disabled after enable")
	}

	var enabled int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT enabled FROM route_groups WHERE name = 'mcp'`).Scan(&enabled); err != nil {
		t.Fatalf("query mcp row: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("mcp enabled in db = %d, want 1", enabled)
	}
}

func TestHydrateRouteGroupsAppliesEnvOverrides(t *testing.T) {
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: t.TempDir() + "/route-hydrate.db",
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.SQL().ExecContext(ctx, `UPDATE route_groups SET enabled = 1 WHERE name = 'mcp'`); err != nil {
		t.Fatalf("seed mcp enabled: %v", err)
	}

	handler := New(config.Config{
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
			domain.RouteGroupMCP:  false,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{
			domain.RouteGroupMCP: false,
		},
	}, db)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mcp status = %d, want env override to keep it disabled", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "route_group_disabled" {
		t.Fatalf("error code = %q, want route_group_disabled", body.Error.Code)
	}

	var enabled int
	if err := db.SQL().QueryRowContext(ctx, `SELECT enabled FROM route_groups WHERE name = 'mcp'`).Scan(&enabled); err != nil {
		t.Fatalf("query mcp: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("mcp enabled after hydrate = %d, want 0", enabled)
	}
}

func TestHydrateRouteGroupsRestoresDBWithoutEnvOverride(t *testing.T) {
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: t.TempDir() + "/route-restore.db",
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.SQL().ExecContext(ctx, `UPDATE route_groups SET enabled = 1 WHERE name IN ('mcp', 'rest')`); err != nil {
		t.Fatalf("seed routes: %v", err)
	}

	handler := New(config.Config{
		Routes:            domainDefaultRoutesDisabledExcept(),
		RouteEnvOverrides: map[domain.RouteGroup]bool{},
	}, db)

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"method":"tools/list"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code == "route_group_disabled" {
		t.Fatal("mcp should be restored from database when env override is absent")
	}
}

func domainDefaultRoutesDisabledExcept() map[domain.RouteGroup]bool {
	routes := map[domain.RouteGroup]bool{}
	for _, group := range domain.AllRouteGroups {
		routes[group] = false
	}
	return routes
}
