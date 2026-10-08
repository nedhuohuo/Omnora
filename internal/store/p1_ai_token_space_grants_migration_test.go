package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenSQLiteAppliesAITokenSpaceGrantsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-token.db")
	seedSQLiteAtMigration(t, path, 12)

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO accounts(id, email, display_name, role, status, password_hash, created_at, updated_at)
VALUES ('acct-token', 'token@example.com', 'Token Owner', 'member', 'active', 'hash', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES
  ('space-token', 'personal', 'Token Space', 'acct-token', 'active'),
  ('space-other', 'shared', 'Other Space', NULL, 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-token', 'acct-token', 'manager');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status)
VALUES
  ('mount-token', 'space-token', 'Token Mount', '/tmp/token', 'managed', 'read_write', 'active'),
  ('mount-other', 'space-other', 'Other Mount', '/tmp/other', 'managed', 'read_write', 'active');
INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, credential_generation, created_at, expires_at, updated_at)
VALUES ('token-legacy', 'ait_legacy', 'sha256:legacy', 'acct-token', 'Legacy', '["files:list"]', 1, CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP);
`); err != nil {
		raw.Close()
		t.Fatalf("seed legacy token: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err := OpenSQLite(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer db.Close()

	var model string
	if err := db.SQL().QueryRow(`SELECT authorization_model FROM ai_tokens WHERE id = 'token-legacy'`).Scan(&model); err != nil {
		t.Fatalf("query legacy authorization model: %v", err)
	}
	if model != "legacy_boundary" {
		t.Fatalf("legacy authorization_model = %q, want legacy_boundary", model)
	}
	var defaultValue sql.NullString
	if err := db.SQL().QueryRow(`
SELECT dflt_value
FROM pragma_table_info('ai_token_folder_denies')
WHERE name = 'identity_fingerprint'
`).Scan(&defaultValue); err != nil {
		t.Fatalf("query identity fingerprint default: %v", err)
	}
	if defaultValue.Valid {
		t.Fatalf("identity_fingerprint default = %q, want no default", defaultValue.String)
	}
	if _, err := db.SQL().Exec(`UPDATE ai_tokens SET authorization_model = 'unknown' WHERE id = 'token-legacy'`); err == nil {
		t.Fatal("authorization_model accepted an unknown value")
	}

	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "ai_token_space_grants"},
		{kind: "table", name: "ai_token_folder_denies"},
		{kind: "index", name: "mounts_id_space_unique"},
		{kind: "index", name: "ai_token_space_grants_space_idx"},
		{kind: "index", name: "ai_token_folder_denies_mount_idx"},
	} {
		var count int
		if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("query %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("%s %s count = %d, want 1", object.kind, object.name, count)
		}
	}

	if _, err := db.SQL().Exec(`
INSERT INTO ai_token_space_grants(token_id, space_id, permission)
VALUES ('token-legacy', 'space-token', 'viewer')
`); err != nil {
		t.Fatalf("insert token grant: %v", err)
	}
	if _, err := db.SQL().Exec(`
INSERT INTO ai_token_space_grants(token_id, space_id, permission)
VALUES ('token-legacy', 'space-other', 'manager')
`); err == nil {
		t.Fatal("space grant accepted manager permission")
	}
	for _, value := range []any{nil, "", "   "} {
		if _, err := db.SQL().Exec(`
INSERT INTO ai_token_folder_denies(token_id, space_id, mount_id, relative_path, identity_fingerprint, status)
VALUES ('token-legacy', 'space-token', 'mount-token', ?, ?, 'active')
`, "invalid-fingerprint-"+strings.TrimSpace(fmt.Sprint(value)), value); err == nil {
			t.Fatalf("folder deny accepted invalid identity fingerprint %#v", value)
		}
	}
	if _, err := db.SQL().Exec(`
INSERT INTO ai_token_folder_denies(token_id, space_id, mount_id, relative_path, identity_fingerprint, status)
VALUES ('token-legacy', 'space-token', 'mount-other', 'private', 'dev:1:ino:1', 'active')
	`); err == nil {
		t.Fatal("folder deny accepted a mount from another space")
	}
	if _, err := db.SQL().Exec(`
INSERT INTO ai_token_folder_denies(token_id, space_id, mount_id, relative_path, identity_fingerprint, status)
VALUES ('token-legacy', 'space-token', 'mount-token', 'private', 'dev:1:ino:1', 'unknown')
`); err == nil {
		t.Fatal("folder deny accepted an unknown status")
	}

	var migrationName string
	if err := db.SQL().QueryRow(`SELECT name FROM schema_migrations WHERE version = 13`).Scan(&migrationName); err != nil {
		t.Fatalf("query migration 013: %v", err)
	}
	if migrationName != "013_ai_token_space_grants.sql" {
		t.Fatalf("migration 013 name = %q", migrationName)
	}
}
