package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
)

func TestAdminMountRoutesUseAccountMountContract(t *testing.T) {
	db, _ := newAPITestServer(t)
	initialAdmin, member := createAPITestAccounts(t, db)
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".admin-mount-external-")
	if err != nil {
		t.Fatalf("create external root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, "photos"), 0o755); err != nil {
		t.Fatalf("create photos root: %v", err)
	}
	handler := New(config.Config{
		Storage:           config.StorageConfig{PredeclaredMountRoot: root},
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
	payload := map[string]any{
		"displayName": "Photos", "rootPath": filepath.Join(root, "photos"),
		"governance": "normal", "mode": "read_only", "indexEnabled": true,
		"grants": []map[string]string{{"accountId": member.ID, "permission": "viewer"}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, initialAdmin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"spaceId"`) || strings.Contains(rec.Body.String(), `"spaceName"`) {
		t.Fatalf("response contains Space contract: %s", rec.Body.String())
	}
	list := authorizedAPITestRequest(t, handler, "/api/v1/admin/mounts", issueAPITestSession(t, db, initialAdmin.ID))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `"spaceId"`) || strings.Contains(list.Body.String(), `"spaceName"`) {
		t.Fatalf("list status/body = %d/%s", list.Code, list.Body.String())
	}
}

func TestAdminMountRoutesRequireAdministrator(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	rec := authorizedAPITestRequest(t, handler, "/api/v1/admin/mounts", issueAPITestSession(t, db, member.ID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestRESTFallbackReturnsGoneForSpaceAndNotFoundForUnknown(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{domain.RouteGroupREST: true}}, nil)
	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{path: "/api/v1/spaces", status: http.StatusGone, code: "space_api_removed"},
		{path: "/api/v1/admin/spaces/legacy", status: http.StatusGone, code: "space_api_removed"},
		{path: "/api/v1/unknown", status: http.StatusNotFound, code: "not_found"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			assertErrorResponse(t, rec, tc.status, tc.code)
		})
	}
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, status, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q", body.Error.Code, code)
	}
}

var _ = context.Background
