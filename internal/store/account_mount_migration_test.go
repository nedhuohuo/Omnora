package store

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestProductionCatalogRegistersAccountMountMigrationOffline(t *testing.T) {
	t.Parallel()
	if len(productionMigrationCatalog) != 15 {
		t.Fatalf("production migration count = %d, want 15", len(productionMigrationCatalog))
	}
	wantAccountMount := migrationDescriptor{Version: 13, Name: "013_account_mount_model.sql", Class: migrationOfflineRequired}
	if got := productionMigrationCatalog[12]; got != wantAccountMount {
		t.Fatalf("account mount migration = %#v, want %#v", got, wantAccountMount)
	}
	got := productionMigrationCatalog[len(productionMigrationCatalog)-1]
	want := migrationDescriptor{Version: 15, Name: "015_backup_artifact_provenance.sql", Class: migrationOnlineSafe}
	if got != want {
		t.Fatalf("latest production migration = %#v, want %#v", got, want)
	}
}

func TestAccountMountMigrationReplacesSpaceBoundaries(t *testing.T) {
	db := newAccountMountMigrationDB(t)
	defer db.Close()

	if err := runMigrations(context.Background(), db, migrationFiles, productionMigrationCatalog, migrationApplyOffline); err != nil {
		t.Fatalf("apply production migration catalog: %v", err)
	}

	for _, table := range []string{"spaces", "space_members"} {
		if tableExists(t, db, table) {
			t.Fatalf("legacy table %q still exists", table)
		}
	}
	rows, err := db.Query(`
SELECT m.name, p.name
FROM sqlite_schema AS m, pragma_table_info(m.name) AS p
WHERE m.type = 'table'
  AND (p.name = 'space_id' OR p.name LIKE '%_space_id')
ORDER BY m.name, p.name`)
	if err != nil {
		t.Fatalf("query legacy space columns: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan legacy space column: %v", err)
		}
		t.Fatalf("legacy space boundary remains: %s.%s", table, column)
	}

	for _, table := range []string{
		"mounts", "personal_directories", "mount_grants", "folder_collaborations",
		"ai_token_boundaries",
		"file_objects", "shares", "upload_sessions", "catalog_entries", "file_operations",
		"mcp_transfer_tickets",
	} {
		if !tableExists(t, db, table) {
			t.Errorf("target table %q does not exist", table)
		}
	}
}

func TestAccountMountMigrationCreatesDefaultMountAndPersonalDirectories(t *testing.T) {
	db := newAccountMountMigrationDB(t)
	defer db.Close()
	if err := runMigrations(context.Background(), db, migrationFiles, productionMigrationCatalog, migrationApplyOffline); err != nil {
		t.Fatalf("apply production migration catalog: %v", err)
	}

	var id, rootPath, purpose, storageKind, governance, mode, status string
	if err := db.QueryRow(`
SELECT id, root_path, purpose, storage_kind, governance, mode, status
FROM mounts WHERE purpose = 'personal_default'`).Scan(
		&id, &rootPath, &purpose, &storageKind, &governance, &mode, &status,
	); err != nil {
		t.Fatalf("query personal default mount: %v", err)
	}
	if got, want := strings.Join([]string{id, rootPath, purpose, storageKind, governance, mode, status}, "|"),
		"personal-default|personal|personal_default|managed|system|read_write|active"; got != want {
		t.Fatalf("default mount = %q, want %q", got, want)
	}

	type personalDirectory struct{ path, state string }
	got := make(map[string]personalDirectory)
	rows, err := db.Query(`SELECT account_id, relative_path, state FROM personal_directories ORDER BY account_id`)
	if err != nil {
		t.Fatalf("query personal directories: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, path, state string
		if err := rows.Scan(&accountID, &path, &state); err != nil {
			t.Fatalf("scan personal directory: %v", err)
		}
		got[accountID] = personalDirectory{path: path, state: state}
	}
	want := map[string]personalDirectory{
		"account-active":  {path: "account-active", state: "ready"},
		"account-deleted": {path: "account-deleted", state: "retained"},
	}
	if len(got) != len(want) {
		t.Fatalf("personal directory count = %d, want %d", len(got), len(want))
	}
	for accountID, wantDirectory := range want {
		if got[accountID] != wantDirectory {
			t.Errorf("personal directory %q = %#v, want %#v", accountID, got[accountID], wantDirectory)
		}
	}

	if _, err := db.Exec(`INSERT INTO mounts(
		id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status
	) VALUES ('second-default', 'Second default', 'other', 'personal_default', 'managed', 'system', 'read_write', 1, 'active')`); err == nil {
		t.Fatal("second personal_default mount was accepted")
	}
	if _, err := db.Exec(`INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('personal-default', 'account-active', 'viewer')`); err == nil {
		t.Fatal("grant on personal_default mount was accepted")
	}
}

func TestAccountMountMigrationEnforcesMountSharePolicy(t *testing.T) {
	db := newAccountMountMigrationDB(t)
	defer db.Close()
	if err := runMigrations(context.Background(), db, migrationFiles, productionMigrationCatalog, migrationApplyOffline); err != nil {
		t.Fatalf("apply production migration catalog: %v", err)
	}

	var notNull int
	var defaultValue sql.NullString
	if err := db.QueryRow(`
SELECT "notnull", dflt_value
FROM pragma_table_info('mounts')
WHERE name = 'share_enabled'`).Scan(&notNull, &defaultValue); err != nil {
		t.Fatalf("query mounts.share_enabled schema: %v", err)
	}
	if notNull != 1 || !defaultValue.Valid || defaultValue.String != "1" {
		t.Fatalf("mounts.share_enabled schema = notnull:%d default:%q, want notnull:1 default:1", notNull, defaultValue.String)
	}

	var personalShareEnabled int
	if err := db.QueryRow(`SELECT share_enabled FROM mounts WHERE purpose = 'personal_default'`).Scan(&personalShareEnabled); err != nil {
		t.Fatalf("query personal default share policy: %v", err)
	}
	if personalShareEnabled != 1 {
		t.Fatalf("personal default share_enabled = %d, want 1", personalShareEnabled)
	}
	if _, err := db.Exec(`UPDATE mounts SET share_enabled = 0 WHERE purpose = 'personal_default'`); err == nil {
		t.Fatal("personal default mount accepted share_enabled = 0")
	}

	if _, err := db.Exec(`INSERT INTO mounts(
		id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status
	) VALUES ('common-share-default', 'Common share default', '/external/share-default', 'common', 'external', 'normal', 'read_write', 0, 'active')`); err != nil {
		t.Fatalf("insert common mount with default share policy: %v", err)
	}
	var commonShareEnabled int
	if err := db.QueryRow(`SELECT share_enabled FROM mounts WHERE id = 'common-share-default'`).Scan(&commonShareEnabled); err != nil {
		t.Fatalf("query common mount default share policy: %v", err)
	}
	if commonShareEnabled != 1 {
		t.Fatalf("common mount default share_enabled = %d, want 1", commonShareEnabled)
	}
	if _, err := db.Exec(`UPDATE mounts SET share_enabled = 0 WHERE id = 'common-share-default'`); err != nil {
		t.Fatalf("disable common mount sharing: %v", err)
	}
	if _, err := db.Exec(`UPDATE mounts SET share_enabled = 2 WHERE id = 'common-share-default'`); err == nil {
		t.Fatal("common mount accepted invalid share_enabled = 2")
	}
}

func TestAccountMountMigrationEnforcesContentPermissionAndClassification(t *testing.T) {
	db := newAccountMountMigrationDB(t)
	defer db.Close()
	if err := runMigrations(context.Background(), db, migrationFiles, productionMigrationCatalog, migrationApplyOffline); err != nil {
		t.Fatalf("apply production migration catalog: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO mounts(
		id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status
	) VALUES ('common-zero-grant', 'Common zero grant', '/external/common', 'common', 'external', 'normal', 'read_write', 0, 'active')`); err != nil {
		t.Fatalf("insert zero-grant common mount: %v", err)
	}
	var grants int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mount_grants WHERE mount_id = 'common-zero-grant'`).Scan(&grants); err != nil {
		t.Fatalf("count common mount grants: %v", err)
	}
	if grants != 0 {
		t.Fatalf("new common mount grant count = %d, want 0", grants)
	}

	invalidMounts := []string{
		`('bad-managed-common', 'Bad managed common', '/bad/one', 'common', 'managed', 'normal', 'read_write', 0, 'active')`,
		`('bad-normal-default', 'Bad normal default', '/bad/two', 'personal_default', 'managed', 'normal', 'read_write', 1, 'active')`,
		`('bad-managed-restricted', 'Bad restricted managed', '/bad/three', 'common', 'managed', 'restricted', 'read_write', 0, 'active')`,
	}
	for _, values := range invalidMounts {
		if _, err := db.Exec(`INSERT INTO mounts(
			id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status
		) VALUES ` + values); err == nil {
			t.Errorf("invalid mount classification %s was accepted", values)
		}
	}

	if _, err := db.Exec(`INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('common-zero-grant', 'account-active', 'manager')`); err == nil {
		t.Fatal("manager mount grant was accepted")
	}
	if _, err := db.Exec(`INSERT INTO ai_tokens(
		id, public_id, secret_hash, account_id, name, scopes, expires_at
	) VALUES ('token-boundary', 'pub-boundary', 'secret', 'account-active', 'Boundary', 'files:read', '2030-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert AI token for boundary checks: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ai_token_boundaries(token_id, source, mount_id, relative_path)
		VALUES ('token-boundary', 'all_account_content', NULL, '')`); err != nil {
		t.Fatalf("insert dynamic all-account-content boundary: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ai_token_boundaries(token_id, source, mount_id, relative_path)
		VALUES ('token-boundary', 'common_mount', NULL, '')`); err == nil {
		t.Fatal("common_mount token boundary without mount was accepted")
	}
	if _, err := db.Exec(`INSERT INTO ai_token_boundaries(token_id, source, mount_id, relative_path)
		VALUES ('token-boundary', 'personal', 'common-zero-grant', '')`); err == nil {
		t.Fatal("personal token boundary with mount was accepted")
	}
	if _, err := db.Exec(`INSERT INTO folder_collaborations(
		id, owner_account_id, recipient_account_id, root_relative_path, root_identity_json, permission, created_by_account_id
	) VALUES ('collab-manager', 'account-active', 'account-deleted', '', '{}', 'manager', 'account-active')`); err == nil {
		t.Fatal("manager folder collaboration was accepted")
	}
}

func newAccountMountMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		t.Fatalf("enable foreign keys: %v", err)
	}

	legacyFS := fstest.MapFS{}
	legacyCatalog := make([]migrationDescriptor, 0, 12)
	for _, descriptor := range productionMigrationCatalog {
		if descriptor.Version > 12 {
			continue
		}
		body, readErr := fs.ReadFile(migrationFiles, "migrations/"+descriptor.Name)
		if readErr != nil {
			db.Close()
			t.Fatalf("read legacy migration %s: %v", descriptor.Name, readErr)
		}
		legacyFS["migrations/"+descriptor.Name] = &fstest.MapFile{Data: body}
		legacyCatalog = append(legacyCatalog, descriptor)
	}
	if err := runMigrations(context.Background(), db, legacyFS, legacyCatalog, migrationApplyOffline); err != nil {
		db.Close()
		t.Fatalf("create schema through migration 012: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES
	('account-active', 'active@example.test', 'Active', 'member', 'active'),
	('account-deleted', 'deleted@example.test', 'Deleted', 'member', 'deleted');
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('legacy-space', 'shared', 'Legacy', 'account-active', 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('legacy-space', 'account-active', 'manager');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status)
VALUES ('legacy-mount', 'legacy-space', 'Legacy mount', '/legacy/files', 'external', 'read_write', 1, 'active');
`); err != nil {
		db.Close()
		t.Fatalf("seed legacy account-mount rows: %v", err)
	}
	return db
}

func openSQLiteThroughMigration(t *testing.T, opts SQLiteOptions, maxVersion int) (*DB, error) {
	t.Helper()
	testFS := fstest.MapFS{}
	catalog := make([]migrationDescriptor, 0, maxVersion)
	for _, descriptor := range productionMigrationCatalog {
		if descriptor.Version > maxVersion {
			continue
		}
		body, err := fs.ReadFile(migrationFiles, "migrations/"+descriptor.Name)
		if err != nil {
			t.Fatalf("read migration %s: %v", descriptor.Name, err)
		}
		testFS["migrations/"+descriptor.Name] = &fstest.MapFile{Data: body}
		catalog = append(catalog, descriptor)
	}
	return openSQLiteWithMigrationSet(context.Background(), opts, testFS, catalog, migrationApplyOnlineSafe)
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("query table %q: %v", name, err)
	}
	return count == 1
}
