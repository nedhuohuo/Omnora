package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestScanBatchIndexesMetadataAndSkipsUnsafeEntries(t *testing.T) {
	db := newCatalogDB(t)
	root := t.TempDir()
	for i := range 205 {
		writeFile(t, filepath.Join(root, fmt.Sprintf("file-%03d.txt", i)), "hello")
	}
	mkdir(t, filepath.Join(root, ".omnora"))
	writeFile(t, filepath.Join(root, ".omnora", "secret.txt"), "secret")
	writeFile(t, filepath.Join(root, "target.txt"), "target")
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	mount := Mount{
		ID:               "mnt_active",
		SpaceID:          "space_1",
		Root:             root,
		Status:           MountStatusActive,
		IndexEnabled:     true,
		IdentityVerified: true,
	}
	insertMount(t, db, mount, "Active", 1, `{"method":"test"}`)

	first, err := NewService(db).ScanBatch(context.Background(), mount, ScanOptions{BatchSize: MinBatchSize})
	if err != nil {
		t.Fatalf("ScanBatch() error = %v", err)
	}
	if first.Done {
		t.Fatalf("first batch Done = true, want false")
	}
	if first.Indexed != MinBatchSize {
		t.Fatalf("first batch Indexed = %d, want %d", first.Indexed, MinBatchSize)
	}

	second, err := NewService(db).ScanBatch(context.Background(), mount, ScanOptions{BatchSize: MinBatchSize, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second ScanBatch() error = %v", err)
	}
	if !second.Done {
		t.Fatalf("second batch Done = false, want true")
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(1) FROM catalog_entries WHERE mount_id = ?", mount.ID).Scan(&count); err != nil {
		t.Fatalf("count catalog entries: %v", err)
	}
	if count != 206 {
		t.Fatalf("catalog entry count = %d, want 206", count)
	}

	for _, relativePath := range []string{".omnora/secret.txt", "linked.txt"} {
		if err := db.QueryRow("SELECT COUNT(1) FROM catalog_entries WHERE relative_path = ?", relativePath).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", relativePath, err)
		}
		if count != 0 {
			t.Fatalf("relative path %q was indexed", relativePath)
		}
	}
}

func TestSearchUsesFilenameCursorAndExcludedMountReasons(t *testing.T) {
	db := newCatalogDB(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "alpha.txt"), "a")
	writeFile(t, filepath.Join(root, "beta.txt"), "b")
	writeFile(t, filepath.Join(root, "gamma.pdf"), "g")

	mount := Mount{
		ID:               "mnt_active",
		SpaceID:          "space_1",
		Root:             root,
		Status:           MountStatusActive,
		IndexEnabled:     true,
		IdentityVerified: true,
	}
	insertMount(t, db, mount, "Active", 1, `{"method":"test"}`)
	insertMount(t, db, Mount{ID: "mnt_disabled", SpaceID: "space_1", Status: MountStatusDisabled}, "Disabled", 1, `{"method":"test"}`)
	insertMount(t, db, Mount{ID: "mnt_no_index", SpaceID: "space_1", Status: MountStatusActive}, "No index", 0, `{"method":"test"}`)
	insertMount(t, db, Mount{ID: "mnt_unverified", SpaceID: "space_1", Status: MountStatusActive}, "Unverified", 1, "")

	if _, err := NewService(db).ScanMount(context.Background(), mount, ScanOptions{}); err != nil {
		t.Fatalf("ScanMount() error = %v", err)
	}

	first, err := NewService(db).Search(context.Background(), SearchOptions{SpaceID: "space_1", Query: ".txt", Limit: 1})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Name != "alpha.txt" {
		t.Fatalf("first search items = %#v, want alpha.txt", first.Items)
	}
	if first.NextCursor == "" {
		t.Fatalf("NextCursor is empty, want cursor")
	}

	second, err := NewService(db).Search(context.Background(), SearchOptions{SpaceID: "space_1", Query: ".txt", Limit: 10, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second Search() error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Name != "beta.txt" {
		t.Fatalf("second search items = %#v, want beta.txt", second.Items)
	}

	reasons := map[string]string{}
	for _, excluded := range first.ExcludedMounts {
		reasons[excluded.MountID] = excluded.Reason
	}
	wantReasons := map[string]string{
		"mnt_disabled":   "mount_disabled",
		"mnt_no_index":   "index_disabled",
		"mnt_unverified": "mount_identity_unverifiable",
	}
	for mountID, want := range wantReasons {
		if got := reasons[mountID]; got != want {
			t.Fatalf("excluded reason for %s = %q, want %q", mountID, got, want)
		}
	}
}

func TestSearchHonorsMountAndRelativePathBoundaries(t *testing.T) {
	db := newCatalogDB(t)
	rootA := t.TempDir()
	rootB := t.TempDir()
	mkdir(t, filepath.Join(rootA, "docs"))
	mkdir(t, filepath.Join(rootB, "docs"))
	writeFile(t, filepath.Join(rootA, "docs", "inside.txt"), "inside")
	writeFile(t, filepath.Join(rootA, "outside.txt"), "outside")
	writeFile(t, filepath.Join(rootB, "docs", "other.txt"), "other")

	mountA := Mount{
		ID:               "mnt_a",
		SpaceID:          "space_1",
		Root:             rootA,
		Status:           MountStatusActive,
		IndexEnabled:     true,
		IdentityVerified: true,
	}
	mountB := Mount{
		ID:               "mnt_b",
		SpaceID:          "space_1",
		Root:             rootB,
		Status:           MountStatusActive,
		IndexEnabled:     true,
		IdentityVerified: true,
	}
	insertMount(t, db, mountA, "A", 1, `{"method":"test"}`)
	insertMount(t, db, mountB, "B", 1, `{"method":"test"}`)
	if _, err := NewService(db).ScanMount(context.Background(), mountA, ScanOptions{}); err != nil {
		t.Fatalf("ScanMount(A) error = %v", err)
	}
	if _, err := NewService(db).ScanMount(context.Background(), mountB, ScanOptions{}); err != nil {
		t.Fatalf("ScanMount(B) error = %v", err)
	}

	result, err := NewService(db).Search(context.Background(), SearchOptions{
		SpaceID: "space_1",
		Query:   ".txt",
		Limit:   10,
		Boundaries: []SearchBoundary{{
			MountID:      "mnt_a",
			RelativePath: "docs",
		}},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].RelativePath != "docs/inside.txt" {
		t.Fatalf("items = %#v, want only docs/inside.txt", result.Items)
	}
}

func newCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	space_id TEXT NOT NULL,
	display_name TEXT NOT NULL,
	root_path TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'external',
	mode TEXT NOT NULL DEFAULT 'read_only',
	index_enabled INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'pending',
	mount_identity_json TEXT
);
CREATE TABLE catalog_entries (
	id TEXT PRIMARY KEY,
	space_id TEXT NOT NULL,
	mount_id TEXT NOT NULL,
	relative_path TEXT NOT NULL,
	name TEXT NOT NULL,
	entry_kind TEXT NOT NULL,
	preview_kind TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	modified_at TEXT NOT NULL,
	identity_fingerprint TEXT NOT NULL,
	indexed_at TEXT NOT NULL,
	deleted_at TEXT,
	UNIQUE (mount_id, relative_path)
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func insertMount(t *testing.T, db *sql.DB, mount Mount, name string, indexEnabled int, identity string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO mounts(id, space_id, display_name, root_path, index_enabled, status, mount_identity_json)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, mount.ID, mount.SpaceID, name, mount.Root, indexEnabled, mount.Status, identity); err != nil {
		t.Fatalf("insert mount %s: %v", mount.ID, err)
	}
}

func writeFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}

func mkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", name, err)
	}
}
