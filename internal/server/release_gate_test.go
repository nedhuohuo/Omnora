package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDownloadFileHTTPRangeSemantics(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "space-range", "mount-range", admin.ID, "read_write", "external")
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("write alpha.txt: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces/space-range/mounts/mount-range/download?path=alpha.txt", nil)
	req.Header.Set("Range", "bytes=2-5")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range download status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "2345" {
		t.Fatalf("range body = %q, want 2345", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q, want bytes 2-5/10", got)
	}
	if got := rec.Header().Get("ETag"); got == "" {
		t.Fatal("ETag must be set for ranged downloads")
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/api/v1/spaces/space-range/mounts/mount-range/download?path=alpha.txt", nil)
	invalidReq.Header.Set("Range", "bytes=100-")
	invalidReq.AddCookie(adminCookie)
	invalidRec := httptest.NewRecorder()
	handler.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status = %d, body = %s", invalidRec.Code, invalidRec.Body.String())
	}
	if got := invalidRec.Header().Get("Content-Range"); got != "bytes */10" {
		t.Fatalf("invalid range Content-Range = %q, want bytes */10", got)
	}
	if !strings.Contains(invalidRec.Body.String(), "invalid_range") {
		t.Fatalf("invalid range body = %s, want stable error code", invalidRec.Body.String())
	}
}

func TestUploadHTTPResumeCompleteAndPermissionRevocation(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "space-upload", "mount-upload", admin.ID, "read_write", "external")
	adminCookie := issueAPITestSession(t, db, admin.ID)

	uploadID := createHTTPUpload(t, handler, adminCookie, "space-upload", "mount-upload", "report.txt", int64(len("hello")))
	putHTTPUploadPart(t, handler, adminCookie, uploadID, 1, "hello", http.StatusNoContent)

	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/"+uploadID, nil)
	statusReq.AddCookie(adminCookie)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", statusRec.Code, statusRec.Body.String())
	}
	var status struct {
		ReceivedSize int64 `json:"receivedSize"`
		Parts        []struct {
			Number int   `json:"number"`
			Size   int64 `json:"size"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode upload status: %v", err)
	}
	if status.ReceivedSize != int64(len("hello")) || len(status.Parts) != 1 || status.Parts[0].Number != 1 {
		t.Fatalf("upload status = %#v, want one resumed part", status)
	}

	completeReq := httptest.NewRequest(http.MethodPost, "/api/v1/uploads/"+uploadID+"/complete", nil)
	completeReq.AddCookie(adminCookie)
	completeRec := httptest.NewRecorder()
	handler.ServeHTTP(completeRec, completeReq)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("complete upload status = %d, body = %s", completeRec.Code, completeRec.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(root, "report.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("completed upload file = %q, err = %v", got, err)
	}

	revokedID := createHTTPUpload(t, handler, adminCookie, "space-upload", "mount-upload", "revoked.txt", int64(len("blocked")))
	if _, err := db.SQL().ExecContext(context.Background(), `
UPDATE space_members
SET permission = 'viewer'
WHERE space_id = ? AND account_id = ?
`, "space-upload", admin.ID); err != nil {
		t.Fatalf("downgrade upload permission: %v", err)
	}
	putHTTPUploadPart(t, handler, adminCookie, revokedID, 1, "blocked", http.StatusForbidden)

	revokedCompleteReq := httptest.NewRequest(http.MethodPost, "/api/v1/uploads/"+revokedID+"/complete", nil)
	revokedCompleteReq.AddCookie(adminCookie)
	revokedCompleteRec := httptest.NewRecorder()
	handler.ServeHTTP(revokedCompleteRec, revokedCompleteReq)
	if revokedCompleteRec.Code != http.StatusForbidden {
		t.Fatalf("revoked complete status = %d, body = %s", revokedCompleteRec.Code, revokedCompleteRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "revoked.txt")); !os.IsNotExist(err) {
		t.Fatalf("revoked upload must not publish target, stat err = %v", err)
	}
}

func createHTTPUpload(t *testing.T, handler http.Handler, cookie *http.Cookie, spaceID, mountID, fileName string, size int64) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"spaceId":    spaceID,
		"mountId":    mountID,
		"parentPath": ".",
		"fileName":   fileName,
		"size":       size,
	})
	if err != nil {
		t.Fatalf("marshal upload create: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create upload status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created upload: %v body=%s", err, rec.Body.String())
	}
	if remaining := time.Until(created.ExpiresAt); remaining < 23*time.Hour {
		t.Fatalf("upload session expires in %s, want existing 24h REST lifetime", remaining)
	}
	return created.ID
}

func putHTTPUploadPart(t *testing.T, handler http.Handler, cookie *http.Cookie, uploadID string, part int, body string, wantStatus int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/uploads/"+uploadID+"/parts/"+strconv.Itoa(part), strings.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("put upload part status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
}
