package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
)

func TestAdminHostDirectorySuggestionsStayInsideConfiguredRoots(t *testing.T) {
	db, _ := newAPITestServer(t)
	root := t.TempDir()
	managed := filepath.Join(root, "managed")
	external := filepath.Join(root, "mounts")
	nested := filepath.Join(external, "photos")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{managed, external, nested, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(external, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	handler := New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage: config.StorageConfig{
			ManagedDir:           managed,
			PredeclaredMountRoot: external,
		},
	}, db)
	admin, member := createAPITestAccounts(t, db)

	t.Run("member forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/host-directories?path=/", nil)
		req.AddCookie(issueAPITestSession(t, db, member.ID))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	adminCookie := issueAPITestSession(t, db, admin.ID)
	t.Run("lists roots", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/host-directories?path=/", nil)
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var payload hostDirectorySuggestions
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(payload.Roots) != 2 {
			t.Fatalf("roots = %#v", payload.Roots)
		}
		if !containsHostPath(payload.Entries, managed) || !containsHostPath(payload.Entries, external) {
			t.Fatalf("entries = %#v", payload.Entries)
		}
		if containsHostPath(payload.Entries, outside) {
			t.Fatalf("outside path leaked: %#v", payload.Entries)
		}
	})

	t.Run("lists children under external root", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/host-directories?path="+external, nil)
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var payload hostDirectorySuggestions
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !containsHostPath(payload.Entries, nested) {
			t.Fatalf("entries = %#v, want %s", payload.Entries, nested)
		}
	})

	t.Run("rejects browsing outside roots", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/host-directories?path="+outside, nil)
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var payload hostDirectorySuggestions
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(payload.Entries) != 0 {
			t.Fatalf("entries = %#v, want empty", payload.Entries)
		}
	})
}

func containsHostPath(entries []hostDirectoryEntry, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func TestProbeMountWritableRejectsReadOnlyRoot(t *testing.T) {
	root := t.TempDir()
	if err := probeMountWritable(root); err != nil {
		t.Fatalf("writable root: %v", err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if err := probeMountWritable(root); err == nil {
		t.Fatal("expected read-only probe to fail")
	}
}

func TestInferMountKindRequiresConfiguredAllowedRoot(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed")
	external := filepath.Join(root, "mounts")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{managed, external, outside} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{cfg: config.Config{Storage: config.StorageConfig{
		ManagedDir: managed, PredeclaredMountRoot: external,
	}}}
	for _, tc := range []struct {
		path string
		kind string
	}{
		{path: filepath.Join(managed, "personal"), kind: "managed"},
		{path: filepath.Join(external, "photos"), kind: "external"},
	} {
		if got, err := s.inferMountKind(tc.path); err != nil || got != tc.kind {
			t.Fatalf("inferMountKind(%q) = %q, %v", tc.path, got, err)
		}
	}
	if _, err := s.inferMountKind(outside); err == nil {
		t.Fatal("expected outside root to be rejected")
	}
}

func TestInferMountKindRejectsOverlappingConfiguredRoots(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "storage")
	external := filepath.Join(managed, "external")
	s := &Server{cfg: config.Config{Storage: config.StorageConfig{
		ManagedDir: managed, PredeclaredMountRoot: external,
	}}}
	if _, err := s.inferMountKind(filepath.Join(external, "photos")); err == nil {
		t.Fatal("expected overlapping configured roots to be rejected")
	}
}

func TestReverifyMountRejectsRootOutsideConfiguredWhitelist(t *testing.T) {
	db, _ := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	createTestSpaceAndMount(t, db, "space-reverify", "mount-reverify", admin.ID, "read_only")
	allowed := filepath.Join(t.TempDir(), "allowed")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	handler := New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage: config.StorageConfig{PredeclaredMountRoot: allowed},
	}, db)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts/mount-reverify/reverify", nil)
	req.AddCookie(issueAPITestSession(t, db, admin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
