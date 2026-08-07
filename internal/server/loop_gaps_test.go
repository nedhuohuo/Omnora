package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/files"
	"omnora/internal/store"
)

// TestTrashPurgeAndEmptyEndpoints exercises the permanent-cleanup HTTP surface.
func TestTrashPurgeAndEmptyEndpoints(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "space-trash2", "mount-trash2", admin.ID, "read_write", "managed")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	trashID := ""
	for _, name := range []string{"a.txt", "b.txt"} {
		delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/spaces/space-trash2/mounts/mount-trash2/object?path="+name, nil)
		delReq.AddCookie(adminCookie)
		delRec := httptest.NewRecorder()
		handler.ServeHTTP(delRec, delReq)
		if delRec.Code != http.StatusOK {
			t.Fatalf("soft delete %s status = %d, body = %s", name, delRec.Code, delRec.Body.String())
		}
		if name == "a.txt" {
			var item struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(delRec.Body.Bytes(), &item); err != nil || item.ID == "" {
				t.Fatalf("decode trash item: %v body=%s", err, delRec.Body.String())
			}
			trashID = item.ID
		}
	}

	purgeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/spaces/space-trash2/mounts/mount-trash2/trash/"+trashID, nil)
	purgeReq.AddCookie(adminCookie)
	purgeRec := httptest.NewRecorder()
	handler.ServeHTTP(purgeRec, purgeReq)
	if purgeRec.Code != http.StatusNoContent {
		t.Fatalf("purge trash status = %d, body = %s", purgeRec.Code, purgeRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("purged file must stay gone, stat err = %v", err)
	}
	unknownPurgeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/spaces/space-trash2/mounts/mount-trash2/trash/missing-trash", nil)
	unknownPurgeReq.AddCookie(adminCookie)
	unknownPurgeRec := httptest.NewRecorder()
	handler.ServeHTTP(unknownPurgeRec, unknownPurgeReq)
	if unknownPurgeRec.Code != http.StatusNotFound {
		t.Fatalf("unknown trash purge status = %d, want 404; body = %s", unknownPurgeRec.Code, unknownPurgeRec.Body.String())
	}

	emptyReq := httptest.NewRequest(http.MethodDelete, "/api/v1/spaces/space-trash2/mounts/mount-trash2/trash", nil)
	emptyReq.AddCookie(adminCookie)
	emptyRec := httptest.NewRecorder()
	handler.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("empty trash status = %d, body = %s", emptyRec.Code, emptyRec.Body.String())
	}
	var emptied struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(emptyRec.Body.Bytes(), &emptied); err != nil {
		t.Fatalf("decode empty result: %v", err)
	}
	if emptied.Removed != 1 {
		t.Fatalf("empty removed = %d, want 1", emptied.Removed)
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/spaces/space-trash2/mounts/mount-trash2/trash", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list trash status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []files.TrashItem `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list trash: %v", err)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("trash should be empty, got %#v", listed.Items)
	}
}

// TestDeleteRevokesChildShares verifies that deleting a directory invalidates
// shares pointing at anything beneath it, not just the exact path.
func TestDeleteRevokesChildShares(t *testing.T) {
	db, handler := newShareAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "space-del", "mount-del", admin.ID, "read_write", "managed")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "report.pdf"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	createShare(t, handler, adminCookie, "space-del", "mount-del", "docs/report.pdf")

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/spaces/space-del/mounts/mount-del/object?path=docs", nil)
	delReq.AddCookie(adminCookie)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete docs status = %d, body = %s", delRec.Code, delRec.Body.String())
	}

	if !shareRevoked(t, db, "space-del", "mount-del", "docs/report.pdf") {
		t.Fatal("child share should be revoked after its parent directory was deleted")
	}
}

// TestCrossMountMoveRevokesChildShares verifies that a cross-mount move of a
// directory invalidates shares beneath it on the source mount.
func TestCrossMountMoveRevokesChildShares(t *testing.T) {
	db, handler := newShareAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	srcRoot := createTestMount(t, db, "space-src", "mount-src", admin.ID, "read_write", "managed")
	if err := os.MkdirAll(filepath.Join(srcRoot, "proj"), 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "proj", "a.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}
	createTestMount(t, db, "space-dst", "mount-dst", admin.ID, "read_write", "managed")
	adminCookie := issueAPITestSession(t, db, admin.ID)

	createShare(t, handler, adminCookie, "space-src", "mount-src", "proj/a.txt")

	moveBody, err := json.Marshal(map[string]any{
		"from":      "proj",
		"toSpaceId": "space-dst",
		"toMountId": "mount-dst",
		"toDir":     ".",
	})
	if err != nil {
		t.Fatalf("marshal move request: %v", err)
	}
	moveReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-src/mounts/mount-src/cross-mount-move", bytes.NewReader(moveBody))
	moveReq.Header.Set("Content-Type", "application/json")
	moveReq.AddCookie(adminCookie)
	moveRec := httptest.NewRecorder()
	handler.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusOK {
		t.Fatalf("cross-mount move status = %d, body = %s", moveRec.Code, moveRec.Body.String())
	}

	if !shareRevoked(t, db, "space-src", "mount-src", "proj/a.txt") {
		t.Fatal("child share should be revoked after its parent directory was moved away")
	}
}

func createShare(t *testing.T, handler http.Handler, cookie *http.Cookie, spaceID, mountID, relativePath string) {
	t.Helper()
	createBody, err := json.Marshal(map[string]any{
		"spaceId":      spaceID,
		"mountId":      mountID,
		"relativePath": relativePath,
		"maxDownloads": 2,
	})
	if err != nil {
		t.Fatalf("marshal create share request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create share status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func shareRevoked(t *testing.T, db *store.DB, spaceID, mountID, relativePath string) bool {
	t.Helper()
	var revoked string
	err := db.SQL().QueryRow(`
SELECT COALESCE(revoked_at, '') FROM shares
WHERE space_id = ? AND mount_id = ? AND relative_path = ? AND revoked_at IS NOT NULL
`, spaceID, mountID, relativePath).Scan(&revoked)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query share revocation: %v", err)
	}
	return revoked != ""
}
