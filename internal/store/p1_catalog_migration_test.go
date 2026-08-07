package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesCatalogJobsMigration(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	for table, column := range map[string]string{
		"catalog_entries": "last_seen_scan_id",
		"jobs":            "claim_token",
	} {
		var count int
		if err := db.SQL().QueryRow(
			"SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = ?",
			table,
			column,
		).Scan(&count); err != nil {
			t.Fatalf("query %s.%s: %v", table, column, err)
		}
		if count != 1 {
			t.Fatalf("column %s.%s count = %d, want 1", table, column, count)
		}
	}
	for _, column := range []string{"lease_expires_at", "heartbeat_at"} {
		var count int
		if err := db.SQL().QueryRow(
			"SELECT COUNT(1) FROM pragma_table_info('jobs') WHERE name = ?",
			column,
		).Scan(&count); err != nil {
			t.Fatalf("query jobs.%s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("column jobs.%s count = %d, want 1", column, count)
		}
	}

	for _, index := range []string{"catalog_entries_scan_epoch_idx", "jobs_lease_claim_idx"} {
		var count int
		if err := db.SQL().QueryRow(
			"SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?",
			index,
		).Scan(&count); err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}

	var migrationName string
	if err := db.SQL().QueryRow("SELECT name FROM schema_migrations WHERE version = 10").Scan(&migrationName); err != nil {
		t.Fatalf("query migration 010: %v", err)
	}
	if migrationName != "010_catalog_jobs.sql" {
		t.Fatalf("migration 010 name = %q", migrationName)
	}
}
