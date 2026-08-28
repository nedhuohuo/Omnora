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

	"omnora/internal/mountid"
)

func TestAdminMountRenameAndDelete(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	ctx := context.Background()

	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".mount-admin-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	keepFile := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keepFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture identity: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-admin', 'personal', 'Admin space', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES ('mnt-keep', 'space-admin', 'huohuo', ?, 'external', 'read_write', 0, 'active', ?)
`, root, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO catalog_entries(id, space_id, mount_id, relative_path, name, entry_kind, preview_kind, size_bytes, modified_at, identity_fingerprint)
VALUES ('ce1', 'space-admin', 'mnt-keep', 'keep.txt', 'keep.txt', 'file', 'text', 4, CURRENT_TIMESTAMP, 'fp')
`); err != nil {
		t.Fatalf("insert catalog: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mount_account_grants(mount_id, account_id, permission) VALUES ('mnt-keep', ?, 'manager')
`, admin.ID); err != nil {
		t.Fatalf("insert mount grant: %v", err)
	}

	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)

	t.Run("member cannot rename", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/mounts/mnt-keep", bytes.NewReader([]byte(`{"displayName":"renamed"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(memberCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("rename mount", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/mounts/mnt-keep", bytes.NewReader([]byte(`{"displayName":"photos"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var name string
		if err := db.SQL().QueryRowContext(ctx, `SELECT display_name FROM mounts WHERE id = 'mnt-keep'`).Scan(&name); err != nil {
			t.Fatalf("load name: %v", err)
		}
		if name != "photos" {
			t.Fatalf("display_name = %q", name)
		}
	})

	t.Run("delete without wiping data", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mounts/mnt-keep", bytes.NewReader([]byte(`{"deleteData":false}`)))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var status string
		if err := db.SQL().QueryRowContext(ctx, `SELECT status FROM mounts WHERE id = 'mnt-keep'`).Scan(&status); err != nil {
			t.Fatalf("load status: %v", err)
		}
		if status != "deleted" {
			t.Fatalf("status = %q", status)
		}
		var catalogCount int
		if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(1) FROM catalog_entries WHERE mount_id = 'mnt-keep'`).Scan(&catalogCount); err != nil {
			t.Fatalf("count catalog: %v", err)
		}
		if catalogCount != 0 {
			t.Fatalf("catalog entries = %d", catalogCount)
		}
		var grantCount int
		if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(1) FROM mount_account_grants WHERE mount_id = 'mnt-keep'`).Scan(&grantCount); err != nil {
			t.Fatalf("count mount grants: %v", err)
		}
		if grantCount != 0 {
			t.Fatalf("mount grants = %d, want 0", grantCount)
		}
		if _, err := os.Stat(keepFile); err != nil {
			t.Fatalf("expected file to remain: %v", err)
		}
	})
}

func TestAdminMountDeleteWipesFolderData(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	ctx := context.Background()

	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".mount-wipe-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	nested := filepath.Join(root, "docs")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(nested, "note.txt")
	if err := os.WriteFile(target, []byte("note"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture identity: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-wipe', 'personal', 'Wipe space', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES ('mnt-wipe', 'space-wipe', 'temp', ?, 'managed', 'read_write', 0, 'active', ?)
`, root, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}

	cookie := issueAPITestSession(t, db, admin.ID)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mounts/mnt-wipe", bytes.NewReader([]byte(`{"deleteData":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries left = %#v", entries)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root should remain: %v", err)
	}
}

func TestWipeMountContentsRejectsUnsafeRoots(t *testing.T) {
	if err := wipeMountContents("/"); err == nil {
		t.Fatal("expected refuse wipe of /")
	}
	if err := wipeMountContents(""); err == nil {
		t.Fatal("expected refuse wipe of empty path")
	}
}
