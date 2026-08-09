package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesRecoveryControlMigration(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	var tableCount int
	if err := db.SQL().QueryRow(
		"SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'recovery_control'",
	).Scan(&tableCount); err != nil {
		t.Fatalf("query recovery_control: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("recovery_control count = %d, want 1", tableCount)
	}

	for table, columns := range map[string][]string{
		"recovery_control": {
			"state", "ready", "request_id", "staging_path", "source_schema_version",
			"safe_snapshot_path", "reason_code", "cleanup_pending", "requested_at", "completed_at",
		},
		"restore_requests": {
			"id", "backup_id", "state", "staging_path", "source_schema_version",
			"safe_snapshot_path", "reason_code", "cleanup_pending", "requested_at", "completed_at",
		},
	} {
		for _, column := range columns {
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
	}

	for _, index := range []string{"restore_requests_state_idx", "restore_requests_cleanup_idx"} {
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

	var configured int
	if err := db.SQL().QueryRow(
		"SELECT configured FROM route_groups WHERE name = 'rest'",
	).Scan(&configured); err != nil {
		t.Fatalf("query route_groups.configured: %v", err)
	}
	if configured != 0 {
		t.Fatalf("new route_groups.configured = %d, want 0", configured)
	}

	var migrationName string
	if err := db.SQL().QueryRow("SELECT name FROM schema_migrations WHERE version = 11").Scan(&migrationName); err != nil {
		t.Fatalf("query migration 011: %v", err)
	}
	if migrationName != "011_recovery_control.sql" {
		t.Fatalf("migration 011 name = %q", migrationName)
	}
}

func TestOpenSQLiteBackfillsLegacyRouteConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-routes.db")
	seedSQLiteAtMigration(t, path, 10)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO accounts(id, email, display_name, role, status, password_hash, created_at, updated_at)
VALUES ('acct-legacy', 'legacy@example.com', 'Legacy', 'admin', 'active', 'hash', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
`); err != nil {
		raw.Close()
		t.Fatalf("insert legacy account: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}
	db, err := openSQLiteThroughMigration(t, SQLiteOptions{Path: path, BusyTimeout: time.Second}, 11)
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()
	var notConfigured int
	if err := db.SQL().QueryRow("SELECT COUNT(1) FROM route_groups WHERE configured = 0").Scan(&notConfigured); err != nil {
		t.Fatalf("query configured route groups: %v", err)
	}
	if notConfigured != 0 {
		t.Fatalf("legacy route groups not configured = %d, want 0", notConfigured)
	}
}
