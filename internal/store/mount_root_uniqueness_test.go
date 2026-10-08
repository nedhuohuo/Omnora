package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesActiveMountRootUniquenessMigration(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "omnora.db"), BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	var migrationName string
	if err := db.SQL().QueryRow("SELECT name FROM schema_migrations WHERE version = 12").Scan(&migrationName); err != nil {
		t.Fatalf("query migration 012: %v", err)
	}
	if migrationName != "012_active_mount_root_uniqueness.sql" {
		t.Fatalf("migration 012 name = %q", migrationName)
	}

	var count int
	if err := db.SQL().QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = 'mounts_active_root_path_unique'").Scan(&count); err != nil {
		t.Fatalf("query root uniqueness index: %v", err)
	}
	if count != 1 {
		t.Fatalf("root uniqueness index count = %d, want 1", count)
	}
}
