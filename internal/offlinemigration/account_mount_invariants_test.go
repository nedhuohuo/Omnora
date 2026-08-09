package offlinemigration

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAccountMountInvariantsAcceptTargetModel(t *testing.T) {
	db := openAccountMountInvariantDB(t)
	assertAccountMountInvariants(t, db, false)
}

func TestAccountMountInvariantsRejectMissingPersonalDirectory(t *testing.T) {
	db := openAccountMountInvariantDB(t)
	if _, err := db.Exec(`DELETE FROM personal_directories WHERE account_id = 'acct-member'`); err != nil {
		t.Fatal(err)
	}
	assertAccountMountInvariants(t, db, true)
}

func TestAccountMountInvariantsRejectSpaceSchema(t *testing.T) {
	db := openAccountMountInvariantDB(t)
	if _, err := db.Exec(`CREATE TABLE spaces (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	assertAccountMountInvariants(t, db, true)
}

func TestAccountMountInvariantsRejectGrantOnDefaultMount(t *testing.T) {
	db := openAccountMountInvariantDB(t)
	if _, err := db.Exec(`INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('personal-default', 'acct-member', 'viewer')`); err != nil {
		t.Fatal(err)
	}
	assertAccountMountInvariants(t, db, true)
}

func assertAccountMountInvariants(t *testing.T, db *sql.DB, wantFailure bool) {
	t.Helper()
	failed := false
	for _, invariant := range AccountMountInvariants() {
		if err := invariant.Check(context.Background(), db); err != nil {
			failed = true
			break
		}
	}
	if failed != wantFailure {
		t.Fatalf("invariants failed = %v, want %v", failed, wantFailure)
	}
}

func openAccountMountInvariantDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL);
CREATE TABLE mounts (
  id TEXT PRIMARY KEY, root_path TEXT NOT NULL, purpose TEXT NOT NULL, storage_kind TEXT NOT NULL,
  governance TEXT NOT NULL, mode TEXT NOT NULL, status TEXT NOT NULL
);
CREATE TABLE personal_directories (
  account_id TEXT PRIMARY KEY, relative_path TEXT UNIQUE NOT NULL, state TEXT NOT NULL
);
CREATE TABLE mount_grants (
  mount_id TEXT NOT NULL, account_id TEXT NOT NULL, permission TEXT NOT NULL,
  PRIMARY KEY(mount_id, account_id)
);
CREATE TABLE folder_collaborations (
  id TEXT PRIMARY KEY, owner_account_id TEXT NOT NULL, recipient_account_id TEXT NOT NULL,
  permission TEXT NOT NULL, revoked_at TEXT
);
INSERT INTO accounts(id, status) VALUES ('acct-admin', 'active'), ('acct-member', 'active'), ('acct-deleted', 'deleted');
INSERT INTO mounts(id, root_path, purpose, storage_kind, governance, mode, status) VALUES
  ('personal-default', 'personal', 'personal_default', 'managed', 'system', 'read_write', 'active'),
  ('mount-common', '/external/common', 'common', 'external', 'normal', 'read_write', 'active');
INSERT INTO personal_directories(account_id, relative_path, state) VALUES
  ('acct-admin', 'acct-admin', 'ready'),
  ('acct-member', 'acct-member', 'ready'),
  ('acct-deleted', 'acct-deleted', 'retained');
INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('mount-common', 'acct-member', 'viewer');
INSERT INTO folder_collaborations(id, owner_account_id, recipient_account_id, permission)
VALUES ('collab-1', 'acct-admin', 'acct-member', 'editor');
`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
