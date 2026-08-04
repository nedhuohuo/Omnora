package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameFileObject(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-1", "mount-1", admin.ID, "read_write")
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	renameBody, err := json.Marshal(map[string]any{
		"from":   "old.txt",
		"toName": "new.txt",
	})
	if err != nil {
		t.Fatalf("marshal rename request: %v", err)
	}
	renameReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-1/mounts/mount-1/rename", bytes.NewReader(renameBody))
	renameReq.Header.Set("Content-Type", "application/json")
	renameReq.AddCookie(adminCookie)
	renameRec := httptest.NewRecorder()
	handler.ServeHTTP(renameRec, renameReq)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", renameRec.Code, renameRec.Body.String())
	}
	var renamed struct {
		RelativePath string `json:"relativePath"`
	}
	if err := json.Unmarshal(renameRec.Body.Bytes(), &renamed); err != nil {
		t.Fatalf("decode rename response: %v", err)
	}
	if renamed.RelativePath != "new.txt" {
		t.Fatalf("renamed relativePath = %q, want %q", renamed.RelativePath, "new.txt")
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Fatalf("expected new.txt to exist on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected old.txt to no longer exist on disk, stat err = %v", err)
	}
}
