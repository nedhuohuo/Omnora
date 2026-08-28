package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesMigrationsAndWAL(t *testing.T) {
	db, err := OpenSQLite(context.Background(), SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.SQL().QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	for _, table := range []string{"accounts", "spaces", "mounts", "mount_account_grants", "audit_events", "route_groups"} {
		var count int
		err := db.SQL().QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count)
		if err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s exists count = %d", table, count)
		}
	}
}

func TestMountGrantMigrationPreservesExistingAccessAndClosesNewMounts(t *testing.T) {
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);`); err != nil {
		t.Fatal(err)
	}
	for version, name := range []string{
		"001_initial_schema.sql",
		"002_control_plane.sql",
		"003_session_entry.sql",
		"004_remove_emergency_access.sql",
		"005_share_fragment_secret.sql",
	} {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name) VALUES (?, ?)`, version+1, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`
INSERT INTO accounts(id, email, display_name, role, status) VALUES ('a1', 'a1@example.test', 'A1', 'member', 'active');
INSERT INTO spaces(id, kind, name, owner_account_id, status) VALUES ('s1', 'personal', 'S1', 'a1', 'active');
INSERT INTO space_members(space_id, account_id, permission) VALUES ('s1', 'a1', 'editor');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status) VALUES ('m1', 's1', 'M1', '/tmp/m1', 'external', 'read_write', 'active');
INSERT INTO shares(id, public_id, secret_hash, fragment_secret, creator_account_id, space_id, mount_id, relative_path, expires_at)
VALUES ('sh1', 'pub1', 'hashed-secret', 'legacy-plaintext-secret', 'a1', 's1', 'm1', 'file.txt', '2099-01-01T00:00:00Z');
`); err != nil {
		t.Fatal(err)
	}
	if err := (&DB{sql: raw}).migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var allow int
	if err := raw.QueryRow(`SELECT allow_public_shares FROM mounts WHERE id = 'm1'`).Scan(&allow); err != nil || allow != 1 {
		t.Fatalf("existing allow_public_shares = %d, err = %v", allow, err)
	}
	var permission string
	if err := raw.QueryRow(`SELECT permission FROM mount_account_grants WHERE mount_id = 'm1' AND account_id = 'a1'`).Scan(&permission); err != nil || permission != "editor" {
		t.Fatalf("backfilled permission = %q, err = %v", permission, err)
	}
	var secretHash string
	var fragmentSecret sql.NullString
	if err := raw.QueryRow(`SELECT secret_hash, fragment_secret FROM shares WHERE id = 'sh1'`).Scan(&secretHash, &fragmentSecret); err != nil {
		t.Fatalf("query migrated share secret: %v", err)
	}
	if secretHash != "hashed-secret" || fragmentSecret.Valid {
		t.Fatalf("migrated share secret hash=%q plaintext=%q valid=%t", secretHash, fragmentSecret.String, fragmentSecret.Valid)
	}
	if _, err := raw.Exec(`UPDATE shares SET fragment_secret = 'plaintext-again' WHERE id = 'sh1'`); err == nil {
		t.Fatal("migration should prevent future plaintext share fragment storage")
	}
	if _, err := raw.Exec(`INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status) VALUES ('m2', 's1', 'M2', '/tmp/m2', 'external', 'read_only', 'active')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT allow_public_shares FROM mounts WHERE id = 'm2'`).Scan(&allow); err != nil || allow != 0 {
		t.Fatalf("new allow_public_shares = %d, err = %v", allow, err)
	}
	var grants int
	if err := raw.QueryRow(`SELECT COUNT(1) FROM mount_account_grants WHERE mount_id = 'm2'`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("new mount grants = %d, err = %v", grants, err)
	}
}
