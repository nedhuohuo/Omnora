package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func TestCreateAndRestoreBackup(t *testing.T) {
	db, _ := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)

	managed := filepath.Join(t.TempDir(), "managed")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir managed: %v", err)
	}
	handler := New(config.Config{
		Database: config.DatabaseConfig{Path: "configured"},
		Storage:  config.StorageConfig{ManagedDir: managed},
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
	}, db)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups", nil)
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create backup status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created backupDTO
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create backup: %v", err)
	}
	if created.Status != "completed" || created.Path == "" || created.Notes != "sqlite online backup" {
		t.Fatalf("created backup = %#v", created)
	}
	if created.CreatedBy != admin.ID || created.CreatedByLabel != "Admin" || created.CreatedByEmail != admin.Email {
		t.Fatalf("created backup creator = %#v, want readable admin labels", created)
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/backups", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list backups status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []backupDTO `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed backups: %v", err)
	}
	if len(listed.Items) == 0 || listed.Items[0].CreatedByLabel != "Admin" || listed.Items[0].CreatedByEmail != admin.Email {
		t.Fatalf("listed backup creator = %#v, want readable admin labels", listed.Items)
	}

	restoreBody, err := json.Marshal(map[string]string{"confirmPhrase": "RESTORE"})
	if err != nil {
		t.Fatalf("marshal restore: %v", err)
	}
	restoreReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/backups/"+created.ID+"/restore", bytes.NewReader(restoreBody))
	restoreReq.Header.Set("Content-Type", "application/json")
	restoreReq.AddCookie(adminCookie)
	restoreRec := httptest.NewRecorder()
	handler.ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", restoreRec.Code, restoreRec.Body.String())
	}
}

func TestManagedSoftDeleteAndTrashRestore(t *testing.T) {
	skipLegacySpaceRESTTest(t)
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "space-trash", "mount-trash", admin.ID, "read_write", "managed")
	if err := os.WriteFile(filepath.Join(root, "doc.txt"), []byte("doc"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/spaces/space-trash/mounts/mount-trash/object?path=doc.txt", nil)
	delReq.AddCookie(adminCookie)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("soft delete status = %d, body = %s", delRec.Code, delRec.Body.String())
	}
	var trashed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(delRec.Body.Bytes(), &trashed); err != nil || trashed.ID == "" {
		t.Fatalf("decode trash item: %v body=%s", err, delRec.Body.String())
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/spaces/space-trash/mounts/mount-trash/trash", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list trash status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	restoreReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-trash/mounts/mount-trash/trash/"+trashed.ID+"/restore", nil)
	restoreReq.AddCookie(adminCookie)
	restoreRec := httptest.NewRecorder()
	handler.ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("restore trash status = %d, body = %s", restoreRec.Code, restoreRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "doc.txt")); err != nil {
		t.Fatalf("expected restored file: %v", err)
	}
}

func createTestMount(t *testing.T, db *store.DB, spaceID, mountID, ownerAccountID, mode, kind string) string {
	t.Helper()
	ctx := context.Background()
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace dir: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".control-plane-test-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	captured, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture mount identity: %v", err)
	}
	identityJSON, err := json.Marshal(captured)
	if err != nil {
		t.Fatalf("marshal mount identity: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES (?, 'shared', 'Test Space', ?, 'active')
`, spaceID, ownerAccountID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO space_members(space_id, account_id, permission)
VALUES (?, ?, 'manager')
`, spaceID, ownerAccountID); err != nil {
		t.Fatalf("insert space member: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, 'Docs', ?, ?, ?, 0, 'active', ?)
`, mountID, spaceID, root, kind, mode, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	return root
}
