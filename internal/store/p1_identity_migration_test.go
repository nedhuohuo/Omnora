package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesIdentitySecurityMigration(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	for table, columns := range map[string][]string{
		"accounts": {
			"totp_pending_secret_ciphertext", "totp_pending_expires_at",
			"password_reset_required", "totp_reset_required",
		},
		"identity_sessions": {"purpose", "reauthenticated_at", "credential_generation"},
		"browser_sessions":  {"credential_generation"},
		"ai_tokens":         {"credential_generation"},
		"audit_events":      {"reason_code", "subject_hash"},
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

	for _, index := range []string{
		"identity_sessions_account_active_idx",
		"audit_events_request_id_idx",
		"audit_events_subject_hash_idx",
	} {
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
	if err := db.SQL().QueryRow(
		"SELECT name FROM schema_migrations WHERE version = 7",
	).Scan(&migrationName); err != nil {
		t.Fatalf("query migration 007: %v", err)
	}
	if migrationName != "007_identity_security.sql" {
		t.Fatalf("migration 007 name = %q", migrationName)
	}
}

func TestOpenSQLiteBackfillsLegacyAuditResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-audit.db")
	seedSQLiteAtMigration(t, path, 6)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO audit_events(action, target_type, metadata_json) VALUES ('legacy_action', 'system', '{}')`); err != nil {
		raw.Close()
		t.Fatalf("insert legacy audit event: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}
	db, err := OpenSQLite(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()
	var result string
	if err := db.SQL().QueryRow("SELECT result FROM audit_events WHERE action = 'legacy_action'").Scan(&result); err != nil {
		t.Fatalf("query legacy audit result: %v", err)
	}
	if result != "success" {
		t.Fatalf("legacy audit result = %q, want success", result)
	}
}
