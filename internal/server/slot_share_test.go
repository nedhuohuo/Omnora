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
)

func TestAdminMountCreateUsesExactExternalDirectory(t *testing.T) {
	db, _ := newAPITestServer(t)
	ctx := context.Background()
	admin, _ := createAPITestAccounts(t, db)

	// Use a workspace-local temp dir: t.TempDir() resolves under /var on macOS,
	// and the mount identity walker rejects symlink path components like /var.
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	base, err := os.MkdirTemp(workspace, ".slot-share-")
	if err != nil {
		t.Fatalf("create temp base: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	managed := filepath.Join(base, "managed")
	external := filepath.Join(base, "mounts")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir managed: %v", err)
	}
	slot1 := filepath.Join(external, "slot1")
	slot2 := filepath.Join(external, "slot2")
	slot3 := filepath.Join(external, "slot3")
	slot4 := filepath.Join(external, "slot4")
	for _, dir := range []string{slot1, slot2, slot3, slot4} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// slot1 is bound with content; slot2 stays empty.
	if err := os.WriteFile(filepath.Join(slot1, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write slot1 file: %v", err)
	}

	handler := New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage: config.StorageConfig{
			ManagedDir:           managed,
			PredeclaredMountRoot: external,
		},
	}, db)

	for _, spaceID := range []string{"space-a", "space-b"} {
		if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES (?, 'shared', ?, ?, 'active')
`, spaceID, spaceID, admin.ID); err != nil {
			t.Fatalf("insert space %s: %v", spaceID, err)
		}
		if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO space_members(space_id, account_id, permission)
VALUES (?, ?, 'manager')
`, spaceID, admin.ID); err != nil {
			t.Fatalf("add admin membership for %s: %v", spaceID, err)
		}
	}

	adminCookie := issueAPITestSession(t, db, admin.ID)
	register := func(spaceID, rootPath, displayName string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"spaceId": spaceID, "displayName": displayName, "rootPath": rootPath,
			"kind": "external", "mode": "read_write", "indexEnabled": false,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("slot root is registered verbatim and keeps existing contents visible", func(t *testing.T) {
		rec := register("space-a", slot1, "NAS Files")
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(slot1, "readme.txt")); err != nil {
			t.Fatalf("existing slot content must remain at the mounted root: %v", err)
		}
		if _, err := os.Stat(filepath.Join(slot1, "space-a")); !os.IsNotExist(err) {
			t.Fatalf("space subdirectory must not be created, stat error = %v", err)
		}
		var rootPath string
		if err := db.SQL().QueryRowContext(ctx, `SELECT root_path FROM mounts WHERE space_id = 'space-a' AND status = 'active'`).Scan(&rootPath); err != nil {
			t.Fatalf("load mount: %v", err)
		}
		if rootPath != slot1 {
			t.Fatalf("root_path = %q, want %q", rootPath, slot1)
		}
		var mountID string
		if err := db.SQL().QueryRowContext(ctx, `SELECT id FROM mounts WHERE space_id = 'space-a' AND root_path = ?`, slot1).Scan(&mountID); err != nil {
			t.Fatalf("load mounted slot id: %v", err)
		}
		listing := authorizedAPITestRequest(t, handler, "/api/v1/spaces/space-a/mounts/"+mountID+"/children?path=.", adminCookie)
		if listing.Code != http.StatusOK || !bytes.Contains(listing.Body.Bytes(), []byte(`"name":"readme.txt"`)) {
			t.Fatalf("mounted root must list original content, status = %d, body = %s", listing.Code, listing.Body.String())
		}
	})

	t.Run("same exact directory cannot belong to another space", func(t *testing.T) {
		rec := register("space-b", slot1, "Duplicate NAS Files")
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if response.Error.Code != "mount_conflict" {
			t.Fatalf("error code = %q, body = %s", response.Error.Code, rec.Body.String())
		}
	})

	t.Run("empty slot is registered verbatim", func(t *testing.T) {
		rec := register("space-b", slot2, "Empty NAS Files")
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(slot2, "space-b")); !os.IsNotExist(err) {
			t.Fatalf("space subdirectory must not be created, stat error = %v", err)
		}
	})

	t.Run("deep path registers as-is", func(t *testing.T) {
		custom := filepath.Join(slot3, "custom")
		if err := os.MkdirAll(custom, 0o755); err != nil {
			t.Fatalf("mkdir custom: %v", err)
		}
		if err := os.WriteFile(filepath.Join(custom, "note.txt"), []byte("n"), 0o644); err != nil {
			t.Fatalf("write custom: %v", err)
		}
		rec := register("space-a", custom, "Custom")
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var rootPath string
		if err := db.SQL().QueryRowContext(ctx, `SELECT root_path FROM mounts WHERE space_id = 'space-a' AND root_path = ?`, custom).Scan(&rootPath); err != nil {
			t.Fatalf("load mount: %v", err)
		}
	})

	t.Run("read-only slot rejected as not writable", func(t *testing.T) {
		if err := os.Chmod(slot4, 0o555); err != nil {
			t.Fatalf("chmod slot4: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(slot4, 0o755) })
		rec := register("space-b", slot4, "Read-only NAS Files")
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if response.Error.Code != "mount_not_writable" {
			t.Fatalf("error code = %q, body = %s", response.Error.Code, rec.Body.String())
		}
	})
}
