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

func TestMountRootAllowedForKindUsesConfiguredRootBoundaries(t *testing.T) {
	server := &Server{cfg: config.Config{Storage: config.StorageConfig{
		ManagedDir:           "/srv/omnora/managed",
		PredeclaredMountRoot: "/mnt/omnora",
	}}}

	for _, tc := range []struct {
		name string
		kind string
		path string
		want bool
	}{
		{name: "managed root", kind: "managed", path: "/srv/omnora/managed", want: true},
		{name: "managed child", kind: "managed", path: "/srv/omnora/managed/photos", want: true},
		{name: "external root", kind: "external", path: "/mnt/omnora", want: true},
		{name: "external child", kind: "external", path: "/mnt/omnora/photos", want: true},
		{name: "outside roots", kind: "external", path: "/etc", want: false},
		{name: "cross kind managed on external", kind: "managed", path: "/mnt/omnora/photos", want: false},
		{name: "cross kind external on managed", kind: "external", path: "/srv/omnora/managed/photos", want: false},
		{name: "managed parent prefix", kind: "managed", path: "/srv/omnora/managed-archive", want: false},
		{name: "external parent prefix", kind: "external", path: "/mnt/omnora-archive", want: false},
		{name: "relative path", kind: "external", path: "mnt/omnora/photos", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := server.mountRootAllowedForKind(tc.kind, tc.path); got != tc.want {
				t.Fatalf("mountRootAllowedForKind(%q, %q) = %v, want %v", tc.kind, tc.path, got, tc.want)
			}
		})
	}

	t.Run("filesystem root configuration is rejected", func(t *testing.T) {
		server.cfg.Storage.PredeclaredMountRoot = string(filepath.Separator)
		if server.mountRootAllowedForKind("external", "/etc") {
			t.Fatal("filesystem root must not make arbitrary container paths eligible")
		}
	})
}

func TestCreateMountEnforcesConfiguredRootForKind(t *testing.T) {
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	base, err := os.MkdirTemp(workspace, ".mount-allowlist-")
	if err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	managed := filepath.Join(base, "managed")
	external := filepath.Join(base, "mounts")
	paths := []string{
		managed,
		filepath.Join(managed, "photos"),
		external,
		filepath.Join(external, "photos"),
		filepath.Join(base, "outside"),
		filepath.Join(base, "mounts-archive"),
		filepath.Join(base, "managed-archive"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}

	for _, tc := range []struct {
		name       string
		kind       string
		rootPath   string
		wantStatus int
		wantCode   string
	}{
		{name: "managed root", kind: "managed", rootPath: managed, wantStatus: http.StatusCreated},
		{name: "managed child", kind: "managed", rootPath: filepath.Join(managed, "photos"), wantStatus: http.StatusCreated},
		{name: "external root", kind: "external", rootPath: external, wantStatus: http.StatusCreated},
		{name: "external child", kind: "external", rootPath: filepath.Join(external, "photos"), wantStatus: http.StatusCreated},
		{name: "outside configured roots", kind: "external", rootPath: filepath.Join(base, "outside"), wantStatus: http.StatusForbidden, wantCode: "mount_root_not_allowed"},
		{name: "managed kind on external root", kind: "managed", rootPath: filepath.Join(external, "photos"), wantStatus: http.StatusForbidden, wantCode: "mount_root_not_allowed"},
		{name: "external kind on managed root", kind: "external", rootPath: filepath.Join(managed, "photos"), wantStatus: http.StatusForbidden, wantCode: "mount_root_not_allowed"},
		{name: "managed parent prefix", kind: "managed", rootPath: filepath.Join(base, "managed-archive"), wantStatus: http.StatusForbidden, wantCode: "mount_root_not_allowed"},
		{name: "external parent prefix", kind: "external", rootPath: filepath.Join(base, "mounts-archive"), wantStatus: http.StatusForbidden, wantCode: "mount_root_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, handler := newMountAllowlistAPITestServer(t, managed, external)
			admin, _ := createAPITestAccounts(t, db)
			if _, err := db.SQL().ExecContext(context.Background(), `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-allowlist', 'shared', 'Allowlist Test', ?, 'active')
`, admin.ID); err != nil {
				t.Fatalf("insert space: %v", err)
			}

			body, err := json.Marshal(map[string]any{
				"spaceId":      "space-allowlist",
				"displayName":  "Mount",
				"rootPath":     tc.rootPath,
				"kind":         tc.kind,
				"mode":         "read_only",
				"indexEnabled": false,
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(issueAPITestSession(t, db, admin.ID))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode == "" {
				return
			}
			var response struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
			}
			if response.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q; body = %s", response.Error.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestCreateReadWriteMountReportsNotWritable(t *testing.T) {
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	base, err := os.MkdirTemp(workspace, ".mount-not-writable-")
	if err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	managed := filepath.Join(base, "managed")
	external := filepath.Join(base, "mounts")
	root := filepath.Join(external, "photos")
	for _, path := range []string{managed, root} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}

	db, handler := newMountAllowlistAPITestServer(t, managed, external)
	admin, _ := createAPITestAccounts(t, db)
	if _, err := db.SQL().ExecContext(context.Background(), `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-not-writable', 'shared', 'Not Writable Test', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}

	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("make mount root read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	body, err := json.Marshal(map[string]any{
		"spaceId":      "space-not-writable",
		"displayName":  "Photos",
		"rootPath":     root,
		"kind":         "external",
		"mode":         "read_write",
		"indexEnabled": false,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/mounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
		t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
	}
	if response.Error.Code != "mount_not_writable" {
		t.Fatalf("error code = %q, want mount_not_writable; body = %s", response.Error.Code, rec.Body.String())
	}
	var count int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(1) FROM mounts WHERE root_path = ?`, root).Scan(&count); err != nil {
		t.Fatalf("count mounts: %v", err)
	}
	if count != 0 {
		t.Fatalf("mount count = %d; rejected mount must not be stored", count)
	}
}

func newMountAllowlistAPITestServer(t *testing.T, managed, external string) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "mount-allowlist-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage: config.StorageConfig{
			ManagedDir:           managed,
			PredeclaredMountRoot: external,
		},
	}, db)
}
