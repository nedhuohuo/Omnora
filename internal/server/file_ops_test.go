package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameFileObject(t *testing.T) {
	skipLegacySpaceRESTTest(t)
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
	if err := os.WriteFile(filepath.Join(root, "occupied.txt"), []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write occupied file: %v", err)
	}
	occupiedReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-1/mounts/mount-1/rename", bytes.NewBufferString(`{"from":"new.txt","toName":"occupied.txt"}`))
	occupiedReq.Header.Set("Content-Type", "application/json")
	occupiedReq.AddCookie(adminCookie)
	occupiedRec := httptest.NewRecorder()
	handler.ServeHTTP(occupiedRec, occupiedReq)
	if occupiedRec.Code != http.StatusBadRequest || !strings.Contains(occupiedRec.Body.String(), "invalid_path") {
		t.Fatalf("occupied rename status/body = %d/%s, want 400 invalid_path", occupiedRec.Code, occupiedRec.Body.String())
	}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatalf("mkdir folder: %v", err)
	}
	moveSelfReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-1/mounts/mount-1/move", bytes.NewBufferString(`{"from":"folder","toDir":"folder"}`))
	moveSelfReq.Header.Set("Content-Type", "application/json")
	moveSelfReq.AddCookie(adminCookie)
	moveSelfRec := httptest.NewRecorder()
	handler.ServeHTTP(moveSelfRec, moveSelfReq)
	if moveSelfRec.Code != http.StatusBadRequest || !strings.Contains(moveSelfRec.Body.String(), "invalid_path") {
		t.Fatalf("self move status/body = %d/%s, want 400 invalid_path", moveSelfRec.Code, moveSelfRec.Body.String())
	}
	missingBody := bytes.NewBufferString(`{"from":"missing.txt","toName":"other.txt"}`)
	missingReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-1/mounts/mount-1/rename", missingBody)
	missingReq.Header.Set("Content-Type", "application/json")
	missingReq.AddCookie(adminCookie)
	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing rename status = %d, want 404; body = %s", missingRec.Code, missingRec.Body.String())
	}
}

func TestCrossMountSameMountKeepsRESTConflict(t *testing.T) {
	skipLegacySpaceRESTTest(t)
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-copy", "mount-copy", admin.ID, "read_write")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	cookie := issueAPITestSession(t, db, admin.ID)
	body := bytes.NewBufferString(`{"from":"source.txt","toSpaceId":"space-copy","toMountId":"mount-copy","toDir":"."}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-copy/mounts/mount-copy/cross-mount-copy", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "source and destination mounts must differ") {
		t.Fatalf("same-mount copy status/body = %d/%s, want 400 stable conflict", rec.Code, rec.Body.String())
	}
	unauthorizedReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-copy/mounts/mount-copy/cross-mount-copy", bytes.NewBufferString(`{"from":"source.txt","toSpaceId":"space-copy","toMountId":"mount-copy","toDir":"."}`))
	unauthorizedReq.Header.Set("Content-Type", "application/json")
	unauthorizedReq.AddCookie(issueAPITestSession(t, db, member.ID))
	unauthorizedRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusForbidden || strings.Contains(unauthorizedRec.Body.String(), "source and destination mounts must differ") {
		t.Fatalf("unauthorized same-mount copy status/body = %d/%s, must fail before relation disclosure", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}
}
