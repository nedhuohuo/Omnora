package server

import (
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

func TestReinstallPreservesOriginalMountPathAccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	cfg := config.Config{
		Database: config.DatabaseConfig{Path: dbPath, BusyTimeout: time.Second},
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
	}

	db := openPersistentAPITestDB(t, dbPath)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "space-reinstall", "mount-reinstall", admin.ID, "read_write", "external")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "note.txt"), []byte("still here"), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
	cookie := issueAPITestSession(t, db, admin.ID)

	firstHandler := New(cfg, db)
	firstRec := authorizedAPITestRequest(t, firstHandler, "/api/v1/spaces/space-reinstall/mounts/mount-reinstall/children?path=docs", cookie)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first listing status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}
	var firstListing struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstListing); err != nil {
		t.Fatalf("decode first listing: %v", err)
	}
	if len(firstListing.Entries) != 1 || firstListing.Entries[0].Name != "note.txt" {
		t.Fatalf("first listing = %#v, want note.txt", firstListing.Entries)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close first db: %v", err)
	}

	reopened := openPersistentAPITestDB(t, dbPath)
	secondHandler := New(cfg, reopened)
	downloadReq := httptest.NewRequest(http.MethodGet, "/api/v1/spaces/space-reinstall/mounts/mount-reinstall/download?path=docs/note.txt", nil)
	downloadReq.AddCookie(cookie)
	downloadRec := httptest.NewRecorder()
	secondHandler.ServeHTTP(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("download after reinstall status = %d, body = %s", downloadRec.Code, downloadRec.Body.String())
	}
	if got := downloadRec.Body.String(); got != "still here" {
		t.Fatalf("download after reinstall body = %q, want original file", got)
	}
}

func openPersistentAPITestDB(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        path,
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open persistent test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
