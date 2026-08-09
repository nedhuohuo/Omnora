package personalstorage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"omnora/internal/mountid"

	_ "modernc.org/sqlite"
)

const targetSchema = `
PRAGMA foreign_keys = ON;
CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL CHECK (status IN ('active', 'disabled', 'deleted')),
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	root_path TEXT NOT NULL,
	purpose TEXT NOT NULL,
	storage_kind TEXT NOT NULL,
	governance TEXT NOT NULL,
	mode TEXT NOT NULL,
	index_enabled INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	mount_identity_json TEXT,
	canonical_root_path TEXT,
	identity_key TEXT,
	mount_source_key TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX mounts_one_personal_default
	ON mounts(purpose) WHERE purpose = 'personal_default' AND status <> 'deleted';
CREATE TABLE personal_directories (
	account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
	relative_path TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL CHECK (state IN ('ready', 'retained')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE mount_grants (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, account_id)
);
CREATE TABLE folder_collaborations (
	id TEXT PRIMARY KEY,
	owner_account_id TEXT NOT NULL REFERENCES personal_directories(account_id),
	recipient_account_id TEXT NOT NULL REFERENCES accounts(id),
	root_relative_path TEXT NOT NULL,
	root_identity_json TEXT NOT NULL,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
	created_by_account_id TEXT NOT NULL REFERENCES accounts(id),
	revoked_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

func TestEnsureDefaultMountAndProvisionAccount(t *testing.T) {
	ctx := context.Background()
	db := openTargetDB(t)
	managed := realTempDir(t)
	svc := New(db, managed)

	mount, err := svc.EnsureDefaultMount(ctx)
	if err != nil {
		t.Fatalf("EnsureDefaultMount() error = %v", err)
	}
	if mount.ID != "personal-default" || mount.RootPath != "personal" {
		t.Fatalf("default mount = %#v", mount)
	}
	if _, err := svc.EnsureDefaultMount(ctx); err != nil {
		t.Fatalf("second EnsureDefaultMount() error = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mounts WHERE purpose = 'personal_default'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("personal default count = %d, error = %v", count, err)
	}

	if _, err := db.Exec(`INSERT INTO accounts(id, status) VALUES ('acct_stable_123', 'active')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory, cleanup, err := svc.ProvisionAccount(ctx, tx, "acct_stable_123")
	if err != nil {
		t.Fatalf("ProvisionAccount() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if directory.RelativePath != "acct_stable_123" {
		t.Fatalf("relative path = %q", directory.RelativePath)
	}
	physical := filepath.Join(managed, "personal", "acct_stable_123")
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		t.Fatalf("personal directory %q: info=%v error=%v", physical, info, err)
	}
}

func TestEnsureDefaultMountDoesNotRecreateMissingRootForExistingAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTargetDB(t)
	managed := realTempDir(t)
	svc := New(db, managed)
	if _, err := svc.EnsureDefaultMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id, status) VALUES ('acct_existing', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO personal_directories(account_id, relative_path, state) VALUES ('acct_existing', 'acct_existing', 'ready')`); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(managed, "personal")
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureDefaultMount(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("EnsureDefaultMount() error = %v, want ErrUnavailable", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing existing root was silently recreated: %v", err)
	}
}

func TestResolvePersonalDirectoryIsAccountScopedAndRetainPreservesFiles(t *testing.T) {
	ctx := context.Background()
	db := openTargetDB(t)
	managed := realTempDir(t)
	svc := New(db, managed)
	if _, err := svc.EnsureDefaultMount(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acct_owner", "acct_admin"} {
		if _, err := db.Exec(`INSERT INTO accounts(id, status) VALUES (?, 'active')`, id); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, cleanup, err := svc.ProvisionAccount(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}
	ownerFile := filepath.Join(managed, "personal", "acct_owner", "private.txt")
	if err := os.WriteFile(ownerFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := svc.Resolve(ctx, "acct_owner")
	if err != nil {
		t.Fatalf("Resolve(owner) error = %v", err)
	}
	if root != filepath.Join(managed, "personal", "acct_owner") {
		t.Fatalf("Resolve(owner) = %q", root)
	}
	adminRoot, err := svc.Resolve(ctx, "acct_admin")
	if err != nil {
		t.Fatalf("Resolve(admin) error = %v", err)
	}
	if adminRoot == root {
		t.Fatalf("different accounts resolved to the same root")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RetainAccount(ctx, tx, "acct_owner"); err != nil {
		t.Fatalf("RetainAccount() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ownerFile); err != nil {
		t.Fatalf("retained account file was removed: %v", err)
	}
	if _, err := svc.Resolve(ctx, "acct_owner"); err == nil {
		t.Fatalf("Resolve(deleted owner) error = nil, want fail-closed")
	}
}

func TestResolveRejectsReplacedPersonalRootIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTargetDB(t)
	managed := realTempDir(t)
	svc := New(db, managed)
	if _, err := svc.EnsureDefaultMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id, status) VALUES ('acct_identity', 'active')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := svc.ProvisionAccount(ctx, tx, "acct_identity")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	personalRoot := filepath.Join(managed, "personal")
	if err := os.Rename(personalRoot, filepath.Join(managed, "personal-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(personalRoot, "acct_identity"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, "acct_identity"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Resolve() after root replacement error = %v, want ErrUnavailable", err)
	}
}

func TestContentSourcesAndPersonalChildrenUseOnlyCurrentAccount(t *testing.T) {
	ctx := context.Background()
	db := openTargetDB(t)
	managed := realTempDir(t)
	svc := New(db, managed)
	if _, err := svc.EnsureDefaultMount(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acct_member", "acct_admin"} {
		if _, err := db.Exec(`INSERT INTO accounts(id, status) VALUES (?, 'active')`, id); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, cleanup, err := svc.ProvisionAccount(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(managed, "personal", "acct_member", "mine.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "personal", "acct_admin", "admin.txt"), []byte("admin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status)
VALUES ('common-1', 'Team NAS', '/mnt/team', 'common', 'external', 'normal', 'read_only', 1, 'active')
`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('common-1', 'acct_member', 'viewer')`); err != nil {
		t.Fatal(err)
	}

	sources, err := svc.ContentSources(ctx, "acct_member")
	if err != nil {
		t.Fatalf("ContentSources() error = %v", err)
	}
	if sources.Personal.Source != "personal" || sources.Personal.Label != "My files" {
		t.Fatalf("personal source = %#v", sources.Personal)
	}
	if len(sources.CommonMounts) != 1 || sources.CommonMounts[0].MountID != "common-1" || sources.CommonMounts[0].Permission != "viewer" {
		t.Fatalf("common mounts = %#v", sources.CommonMounts)
	}
	adminSources, err := svc.ContentSources(ctx, "acct_admin")
	if err != nil {
		t.Fatalf("ContentSources(admin) error = %v", err)
	}
	if len(adminSources.CommonMounts) != 0 {
		t.Fatalf("ungranted admin discovered common mounts: %#v", adminSources.CommonMounts)
	}

	listing, err := svc.ListPersonal(ctx, "acct_member", ".")
	if err != nil {
		t.Fatalf("ListPersonal() error = %v", err)
	}
	names := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
	}
	if !reflect.DeepEqual(names, []string{"mine.txt"}) {
		t.Fatalf("personal entries = %#v, want only member file", names)
	}
}

func TestListCollaborationRequiresLiveRecipientGrantAndRootIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTargetDB(t)
	managed := realTempDir(t)
	svc := New(db, managed)
	if _, err := svc.EnsureDefaultMount(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acct_owner", "acct_recipient", "acct_other"} {
		if _, err := db.Exec(`INSERT INTO accounts(id, status) VALUES (?, 'active')`, id); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, cleanup, err := svc.ProvisionAccount(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}
	sharedRoot := filepath.Join(managed, "personal", "acct_owner", "shared")
	if err := os.Mkdir(sharedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedRoot, "visible.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := mountid.Capture(sharedRoot)
	if err != nil {
		t.Fatal(err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO folder_collaborations(
  id, owner_account_id, recipient_account_id, root_relative_path,
  root_identity_json, permission, created_by_account_id
) VALUES ('collab-1', 'acct_owner', 'acct_recipient', 'shared', ?, 'viewer', 'acct_owner')
`, string(identityJSON)); err != nil {
		t.Fatal(err)
	}

	listing, err := svc.ListCollaboration(ctx, "acct_recipient", "collab-1", ".")
	if err != nil {
		t.Fatalf("ListCollaboration() error = %v", err)
	}
	if !listing.ReadOnly || len(listing.Entries) != 1 || listing.Entries[0].Name != "visible.txt" {
		t.Fatalf("collaboration listing = %#v", listing)
	}
	if _, err := svc.ListCollaboration(ctx, "acct_other", "collab-1", "."); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("other account error = %v, want ErrUnavailable", err)
	}
	if _, err := db.Exec(`UPDATE folder_collaborations SET revoked_at = CURRENT_TIMESTAMP WHERE id = 'collab-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListCollaboration(ctx, "acct_recipient", "collab-1", "."); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("revoked collaboration error = %v, want ErrUnavailable", err)
	}
}

func openTargetDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(targetSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
