package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesFileAndShareMigrations(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	for table, columns := range map[string][]string{
		"upload_sessions": {
			"version", "operation_phase", "lock_token", "lock_epoch", "lease_expires_at",
			"cleanup_pending", "final_identity", "credential_generation",
		},
		"upload_parts": {"checksum", "state", "write_token"},
		"mounts":       {"canonical_root_path", "identity_key", "mount_source_key"},
		"shares": {
			"target_kind", "target_identity_json", "invalidated_at", "invalidated_reason", "credential_generation",
		},
		"share_sessions": {"credential_generation"},
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

	for table := range map[string]bool{
		"file_operations":        true,
		"mount_identity_claims":  true,
		"mount_claim_conflicts":  true,
		"share_download_tickets": true,
	} {
		var count int
		if err := db.SQL().QueryRow(
			"SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}

	for _, index := range []string{
		"file_operations_pending_idx",
		"upload_sessions_lease_cleanup_idx",
		"mount_claim_conflicts_lookup_idx",
		"share_download_tickets_status_expiry_idx",
		"share_download_tickets_share_generation_idx",
		"share_download_tickets_session_status_idx",
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

	for version, want := range map[int]string{8: "008_file_operations_and_uploads.sql", 9: "009_share_tickets_and_identity.sql"} {
		var name string
		if err := db.SQL().QueryRow("SELECT name FROM schema_migrations WHERE version = ?", version).Scan(&name); err != nil {
			t.Fatalf("query migration %d: %v", version, err)
		}
		if name != want {
			t.Fatalf("migration %d name = %q, want %q", version, name, want)
		}
	}
}

func TestP1FileMigrationPreservesLegacyUploadStatusConstraint(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	var createSQL string
	if err := db.SQL().QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'upload_sessions'").Scan(&createSQL); err != nil {
		t.Fatalf("query upload_sessions SQL: %v", err)
	}
	for _, status := range []string{"active", "completed", "canceled", "expired", "failed"} {
		if !strings.Contains(createSQL, "'"+status+"'") {
			t.Fatalf("upload_sessions status constraint missing %q: %s", status, createSQL)
		}
	}
	if strings.Contains(createSQL, "'creating'") {
		t.Fatalf("upload_sessions status constraint must not add creating: %s", createSQL)
	}
}

func TestOpenSQLiteBackfillsLegacyUploadCredentialGenerationIn008(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-upload.db")
	seedSQLiteAtMigration(t, path, 7)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO accounts(id, email, display_name, role, status, password_hash, created_at, updated_at)
VALUES ('acct-upload', 'upload@example.com', 'Upload', 'member', 'active', 'hash', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-upload', 'personal', 'Upload', 'acct-upload', 'active');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status)
VALUES ('mount-upload', 'space-upload', 'Upload', '/tmp/upload', 'managed', 'read_write', 'active');
INSERT INTO upload_sessions(id, account_id, space_id, mount_id, target_relative_path, declared_size, part_size, temp_dir, expires_at)
VALUES ('upload-legacy', 'acct-upload', 'space-upload', 'mount-upload', 'file.txt', 0, 1024, '/tmp/upload', '2030-01-01T00:00:00Z')
`); err != nil {
		raw.Close()
		t.Fatalf("seed legacy upload: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}
	db, err := openSQLiteThroughMigration(t, SQLiteOptions{Path: path, BusyTimeout: time.Second}, 8)
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()
	var generation int64
	if err := db.SQL().QueryRow("SELECT credential_generation FROM upload_sessions WHERE id = 'upload-legacy'").Scan(&generation); err != nil {
		t.Fatalf("query upload generation: %v", err)
	}
	if generation != 1 {
		t.Fatalf("upload credential_generation = %d, want 1", generation)
	}
}
