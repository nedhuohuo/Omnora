package catalog

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/contentref"
	"omnora/internal/store"
)

func catalogDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "catalog.db")})
	if err != nil {
		t.Fatalf("open target database: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle.SQL()
}

func TestScanAndSearchTargetSchemaUsesSourceWithoutSpace(t *testing.T) {
	db := catalogDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status, mount_identity_json)
VALUES ('common-1', 'Common', ?, 'common', 'external', 'normal', 'read_write', 1, 'active', '{}')
`, root); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	service := NewService(db)
	if _, err := service.ScanMount(context.Background(), Mount{
		ID: "common-1", Source: contentref.SourceCommonMount, Root: root,
		Status: MountStatusActive, IndexEnabled: true, IdentityVerified: true,
	}, ScanOptions{}); err != nil {
		t.Fatalf("ScanMount: %v", err)
	}
	result, err := service.Search(context.Background(), SearchOptions{
		Query: "readme", Boundaries: []SearchBoundary{{MountID: "common-1", RelativePath: "docs"}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Source != contentref.SourceCommonMount || result.Items[0].MountID != "common-1" || result.Items[0].RelativePath != "docs/readme.txt" {
		t.Fatalf("items = %#v", result.Items)
	}
}

func TestSearchPersonalEntriesReportsPersonalSourceAndHonorsAccountBoundary(t *testing.T) {
	db := catalogDB(t)
	if _, err := db.Exec(`UPDATE mounts SET mount_identity_json = '{}', index_enabled = 1 WHERE id = 'personal-default'`); err != nil {
		t.Fatalf("enable personal index: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO catalog_entries(id, mount_id, relative_path, name, entry_kind, preview_kind, size_bytes, modified_at, identity_fingerprint)
VALUES ('mine', 'personal-default', 'acct-1/docs/mine.txt', 'mine.txt', 'file', 'text', 1, '2026-08-08T00:00:00Z', 'mine'),
       ('other', 'personal-default', 'acct-2/docs/other.txt', 'other.txt', 'file', 'text', 1, '2026-08-08T00:00:00Z', 'other')
`); err != nil {
		t.Fatalf("insert catalog rows: %v", err)
	}
	result, err := NewService(db).Search(context.Background(), SearchOptions{
		Boundaries: []SearchBoundary{{MountID: "personal-default", RelativePath: "acct-1"}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "mine" || result.Items[0].Source != contentref.SourcePersonal {
		t.Fatalf("items = %#v", result.Items)
	}
}
