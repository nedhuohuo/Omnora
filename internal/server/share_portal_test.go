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
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/store"
)

func newShareAPITestServer(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "share-api-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, New(config.Config{
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST:  true,
			domain.RouteGroupShare: true,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{
			domain.RouteGroupREST:  true,
			domain.RouteGroupShare: true,
		},
	}, db)
}

func TestSharePortalChildrenAfterExchange(t *testing.T) {
	db, handler := newShareAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-1", "mount-1", admin.ID, "read_write")
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	createBody, err := json.Marshal(map[string]any{
		"spaceId":      "space-1",
		"mountId":      "mount-1",
		"relativePath": "docs",
	})
	if err != nil {
		t.Fatalf("marshal create share request: %v", err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create share status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		PublicID string `json:"publicId"`
		Secret   string `json:"secret"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil || created.PublicID == "" || created.Secret == "" {
		t.Fatalf("decode created share: err = %v, body = %s", err, createRec.Body.String())
	}

	exchangeBody, err := json.Marshal(map[string]any{
		"public_id": created.PublicID,
		"secret":    created.Secret,
	})
	if err != nil {
		t.Fatalf("marshal exchange request: %v", err)
	}
	exchangeReq := httptest.NewRequest(http.MethodPost, "/api/v1/share-sessions", bytes.NewReader(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")
	exchangeRec := httptest.NewRecorder()
	handler.ServeHTTP(exchangeRec, exchangeReq)
	if exchangeRec.Code != http.StatusCreated {
		t.Fatalf("exchange status = %d, body = %s", exchangeRec.Code, exchangeRec.Body.String())
	}

	var shareCookie *http.Cookie
	for _, cookie := range exchangeRec.Result().Cookies() {
		if cookie.Name == shareSessionCookieName {
			shareCookie = cookie
			break
		}
	}
	if shareCookie == nil {
		t.Fatalf("expected %s cookie to be set", shareSessionCookieName)
	}
	if shareCookie.Path != "/" {
		t.Fatalf("share session cookie Path = %q, want %q so /api/v1/share/* also receives it", shareCookie.Path, "/")
	}

	childrenReq := httptest.NewRequest(http.MethodGet, "/api/v1/share/children", nil)
	childrenReq.AddCookie(shareCookie)
	childrenRec := httptest.NewRecorder()
	handler.ServeHTTP(childrenRec, childrenReq)
	if childrenRec.Code != http.StatusOK {
		t.Fatalf("share children status = %d, body = %s", childrenRec.Code, childrenRec.Body.String())
	}
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(childrenRec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode share children: %v", err)
	}
	found := false
	for _, entry := range listing.Entries {
		if entry.Name == "notes.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("share children entries = %#v, want notes.txt", listing.Entries)
	}
}

func TestShareCurrentIncludesTargetMetadata(t *testing.T) {
	db, handler := newShareAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-meta", "mount-meta", admin.ID, "read_write")
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("# Report"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)

	createBody, err := json.Marshal(map[string]any{
		"spaceId":      "space-meta",
		"mountId":      "mount-meta",
		"relativePath": "report.md",
	})
	if err != nil {
		t.Fatalf("marshal create share request: %v", err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create share status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		PublicID string `json:"publicId"`
		Secret   string `json:"secret"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created share: %v", err)
	}

	exchangeBody, err := json.Marshal(map[string]any{
		"public_id": created.PublicID,
		"secret":    created.Secret,
	})
	if err != nil {
		t.Fatalf("marshal exchange request: %v", err)
	}
	exchangeReq := httptest.NewRequest(http.MethodPost, "/api/v1/share-sessions", bytes.NewReader(exchangeBody))
	exchangeReq.Header.Set("Content-Type", "application/json")
	exchangeRec := httptest.NewRecorder()
	handler.ServeHTTP(exchangeRec, exchangeReq)
	if exchangeRec.Code != http.StatusCreated {
		t.Fatalf("exchange status = %d, body = %s", exchangeRec.Code, exchangeRec.Body.String())
	}

	currentReq := httptest.NewRequest(http.MethodGet, "/api/v1/share/current", nil)
	currentReq.AddCookie(exchangeRec.Result().Cookies()[0])
	currentRec := httptest.NewRecorder()
	handler.ServeHTTP(currentRec, currentReq)
	if currentRec.Code != http.StatusOK {
		t.Fatalf("current status = %d, body = %s", currentRec.Code, currentRec.Body.String())
	}
	var current struct {
		Path        string `json:"path"`
		Kind        string `json:"kind"`
		PreviewKind string `json:"previewKind"`
		Size        int64  `json:"size"`
		ModifiedAt  string `json:"modifiedAt"`
	}
	if err := json.Unmarshal(currentRec.Body.Bytes(), &current); err != nil {
		t.Fatalf("decode current: %v", err)
	}
	if current.Path != "report.md" || current.Kind != "file" || current.PreviewKind != "markdown" || current.Size != int64(len("# Report")) || current.ModifiedAt == "" {
		t.Fatalf("share current metadata = %#v", current)
	}
}
