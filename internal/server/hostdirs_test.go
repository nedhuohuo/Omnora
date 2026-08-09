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
	skipLegacySpaceRESTTest(t)
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
	if err := os.WriteFile(filepath.Join(nested, "photo.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	// Test directories are not bind mounts, so every external slot stays
	// hidden regardless of content; the bind detection itself is covered by
	// the mountid package tests.
	emptySlot := filepath.Join(external, "empty-slot")
	if err := os.Mkdir(emptySlot, 0o755); err != nil {
		t.Fatalf("mkdir empty slot: %v", err)
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
		if got := hostDirectoryRootKind(payload.RootDetails, managed); got != "managed" {
			t.Fatalf("managed root kind = %q, want managed; details = %#v", got, payload.RootDetails)
		}
		if got := hostDirectoryRootKind(payload.RootDetails, external); got != "external" {
			t.Fatalf("external root kind = %q, want external; details = %#v", got, payload.RootDetails)
		}
		if !containsHostPath(payload.Entries, managed) || !containsHostPath(payload.Entries, external) {
			t.Fatalf("entries = %#v", payload.Entries)
		}
		if got := hostDirectoryEntryKind(payload.Entries, managed); got != "managed" {
			t.Fatalf("managed entry kind = %q, want managed; entries = %#v", got, payload.Entries)
		}
		if got := hostDirectoryEntryKind(payload.Entries, external); got != "external" {
			t.Fatalf("external entry kind = %q, want external; entries = %#v", got, payload.Entries)
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
		if containsHostPath(payload.Entries, nested) {
			t.Fatalf("external slot without a bind mount must not be suggested: %#v", payload.Entries)
		}
		if containsHostPath(payload.Entries, emptySlot) {
			t.Fatalf("external slot without a bind mount must not be suggested: %#v", payload.Entries)
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

func hostDirectoryRootKind(roots []hostDirectoryRoot, path string) string {
	for _, root := range roots {
		if root.Path == path {
			return root.Kind
		}
	}
	return ""
}

func hostDirectoryEntryKind(entries []hostDirectoryEntry, path string) string {
	for _, entry := range entries {
		if entry.Path == path {
			return entry.Kind
		}
	}
	return ""
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
