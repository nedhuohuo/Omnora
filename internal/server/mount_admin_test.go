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
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mounts/mnt-keep", nil)
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
		if _, err := os.Stat(keepFile); err != nil {
			t.Fatalf("expected file to remain: %v", err)
		}
	})
}

func TestAdminMountDeleteRejectsDataDeletion(t *testing.T) {
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
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != "mount_data_delete_disabled" {
		t.Fatalf("error code = %q, body = %s", response.Error.Code, rec.Body.String())
	}
	var status string
	if err := db.SQL().QueryRowContext(ctx, `SELECT status FROM mounts WHERE id = 'mnt-wipe'`).Scan(&status); err != nil {
		t.Fatalf("load status: %v", err)
	}
	if status != "active" {
		t.Fatalf("status = %q; rejected request must not change database", status)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected user file to remain: %v", err)
	}
}

func TestAdminMountReverifyReportsNotWritable(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-reverify", "mnt-reverify", admin.ID, "read_write")
	if _, err := db.SQL().ExecContext(context.Background(), `UPDATE mounts SET status = 'unavailable' WHERE id = 'mnt-reverify'`); err != nil {
		t.Fatalf("mark mount unavailable: %v", err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("make mount root read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts/mnt-reverify/reverify", nil)
	req.AddCookie(issueAPITestSession(t, db, admin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, rec.Body.String())
	}
	if response.Error.Code != "mount_not_writable" {
		t.Fatalf("error code = %q, want mount_not_writable; body = %s", response.Error.Code, rec.Body.String())
	}
	var status string
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT status FROM mounts WHERE id = 'mnt-reverify'`).Scan(&status); err != nil {
		t.Fatalf("load mount status: %v", err)
	}
	if status != "unavailable" {
		t.Fatalf("status = %q; failed re-verification must not reactivate the mount", status)
	}
}
