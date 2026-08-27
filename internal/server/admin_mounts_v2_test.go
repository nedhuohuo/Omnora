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
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/mountid"
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

func TestAdminMountGrantUpdateAndNonDestructiveDelete(t *testing.T) {
	db, _ := newAPITestServer(t)
	initialAdmin, member := createAPITestAccounts(t, db)
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(workspace, ".mount-lifecycle-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(config.Config{
		Storage:           config.StorageConfig{PredeclaredMountRoot: filepath.Dir(root)},
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
	createBody := []byte(`{"displayName":"Lifecycle","rootPath":"` + root + `","governance":"normal","mode":"read_only","indexEnabled":true,"grants":[]}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(issueAPITestSession(t, db, initialAdmin.ID))
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("created mount = %#v, err = %v", created, err)
	}
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/mounts/"+created.ID+"/grants/"+member.ID, bytes.NewReader([]byte(`{"permission":"editor"}`)))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(issueAPITestSession(t, db, initialAdmin.ID))
	putRec := httptest.NewRecorder()
	handler.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("grant status = %d, body = %s", putRec.Code, putRec.Body.String())
	}
	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/mounts/"+created.ID+"/grants", issueAPITestSession(t, db, initialAdmin.ID))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), member.ID) {
		t.Fatalf("grant list status/body = %d/%s", listRec.Code, listRec.Body.String())
	}
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/mounts/"+created.ID, bytes.NewReader([]byte(`{"shareEnabled":false}`)))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.AddCookie(issueAPITestSession(t, db, initialAdmin.ID))
	patchRec := httptest.NewRecorder()
	handler.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK || strings.Contains(patchRec.Body.String(), `"shareEnabled":true`) {
		t.Fatalf("patch status/body = %d/%s", patchRec.Code, patchRec.Body.String())
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mounts/"+created.ID, bytes.NewReader([]byte(`{"displayName":"Lifecycle"}`)))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteReq.AddCookie(issueAPITestSession(t, db, initialAdmin.ID))
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK || !strings.Contains(deleteRec.Body.String(), `"dataDeleted":false`) {
		t.Fatalf("delete status/body = %d/%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("physical file was removed: %v", err)
	}
}

func TestOrdinaryAdminCannotEnqueueRestrictedIndexJob(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`UPDATE accounts SET role = 'admin' WHERE id = ?`, member.ID); err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(workspace, ".restricted-index-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	identity := captureMountIdentity(t, root)
	if _, err := db.SQL().Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, share_enabled, status, mount_identity_json)
VALUES ('restricted-index', 'Restricted Index', ?, 'common', 'external', 'restricted', 'read_only', 1, 1, 'active', ?)
`, root, identity); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"mountId":"restricted-index"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/index-jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, member.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func captureMountIdentity(t *testing.T, root string) string {
	t.Helper()
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestOrdinaryAdminOverviewHidesRestrictedMountHealth(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, ordinary := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`UPDATE accounts SET role = 'admin' WHERE id = ?`, ordinary.ID); err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(workspace, ".restricted-overview-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if _, err := db.SQL().Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, share_enabled, status, mount_identity_json)
VALUES ('restricted-overview', 'Restricted Overview', ?, 'common', 'external', 'restricted', 'read_only', 0, 1, 'active', ?)
`, root, captureMountIdentity(t, root)); err != nil {
		t.Fatal(err)
	}
	rec := authorizedAPITestRequest(t, handler, "/api/v1/admin/overview", issueAPITestSession(t, db, ordinary.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		CommonMounts   int            `json:"commonMounts"`
		MountsByHealth map[string]int `json:"mountsByHealth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CommonMounts != 0 || body.MountsByHealth["active"] != 0 {
		t.Fatalf("restricted mount leaked in overview: %#v", body)
	}
}

func TestAdminOverviewCountsIndexJobsWithoutBlockingOnMountLookup(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	createTestMount(t, db, "overview-job-mount", admin.ID, "read_write")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().Exec(`
INSERT INTO jobs(id, kind, priority, status, payload_json, checkpoint_json, attempts, max_attempts, created_at, updated_at)
VALUES ('overview-job', 'index_mount', 100, 'queued', '{"mount_id":"overview-job-mount"}', '{}', 0, 3, ?, ?)
`, now, now); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/overview", nil).WithContext(ctx)
	req.AddCookie(issueAPITestSession(t, db, admin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		JobsByStatus map[string]int `json:"jobsByStatus"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.JobsByStatus["queued"] != 1 {
		t.Fatalf("jobsByStatus = %#v, want queued job counted", body.JobsByStatus)
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
