package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"
)

func TestMigrationCatalogValidation(t *testing.T) {
	testFS := fstest.MapFS{
		"migrations/001_one.sql": {Data: []byte(`CREATE TABLE one (id INTEGER PRIMARY KEY)`)},
		"migrations/002_two.sql": {Data: []byte(`CREATE TABLE two (id INTEGER PRIMARY KEY)`)},
	}
	valid := []migrationDescriptor{
		{Version: 1, Name: "001_one.sql", Class: migrationOnlineSafe},
		{Version: 2, Name: "002_two.sql", Class: migrationOfflineRequired},
	}

	t.Run("exact catalog is accepted and sorted", func(t *testing.T) {
		got, err := validateMigrationCatalog(testFS, []migrationDescriptor{valid[1], valid[0]})
		if err != nil {
			t.Fatalf("validateMigrationCatalog() error = %v", err)
		}
		if len(got) != 2 || got[0] != valid[0] || got[1] != valid[1] {
			t.Fatalf("validateMigrationCatalog() = %#v, want %#v", got, valid)
		}
	})

	tests := []struct {
		name    string
		fs      fstest.MapFS
		catalog []migrationDescriptor
	}{
		{name: "unregistered SQL file", fs: testFS, catalog: valid[:1]},
		{name: "descriptor without SQL file", fs: testFS, catalog: append(valid, migrationDescriptor{Version: 3, Name: "003_three.sql", Class: migrationOnlineSafe})},
		{name: "duplicate descriptor version", fs: testFS, catalog: []migrationDescriptor{valid[0], {Version: 1, Name: "002_two.sql", Class: migrationOfflineRequired}}},
		{name: "duplicate descriptor name", fs: testFS, catalog: []migrationDescriptor{valid[0], {Version: 2, Name: "001_one.sql", Class: migrationOfflineRequired}}},
		{name: "descriptor version differs from filename", fs: testFS, catalog: []migrationDescriptor{{Version: 2, Name: "001_one.sql", Class: migrationOnlineSafe}, valid[1]}},
		{name: "unknown class", fs: testFS, catalog: []migrationDescriptor{valid[0], {Version: 2, Name: "002_two.sql", Class: migrationClass("unknown")}}},
		{
			name: "duplicate embedded version",
			fs: fstest.MapFS{
				"migrations/001_one.sql":       {Data: []byte(`SELECT 1`)},
				"migrations/001_duplicate.sql": {Data: []byte(`SELECT 1`)},
			},
			catalog: []migrationDescriptor{
				{Version: 1, Name: "001_one.sql", Class: migrationOnlineSafe},
				{Version: 2, Name: "001_duplicate.sql", Class: migrationOnlineSafe},
			},
		},
		{
			name: "catalog version zero",
			fs: fstest.MapFS{
				"migrations/000_zero.sql": {Data: []byte(`SELECT 1`)},
				"migrations/001_one.sql":  {Data: []byte(`SELECT 1`)},
			},
			catalog: []migrationDescriptor{
				{Version: 0, Name: "000_zero.sql", Class: migrationOnlineSafe},
				{Version: 1, Name: "001_one.sql", Class: migrationOnlineSafe},
			},
		},
		{
			name: "catalog version gap",
			fs: fstest.MapFS{
				"migrations/001_one.sql":   {Data: []byte(`SELECT 1`)},
				"migrations/003_three.sql": {Data: []byte(`SELECT 1`)},
			},
			catalog: []migrationDescriptor{
				{Version: 1, Name: "001_one.sql", Class: migrationOnlineSafe},
				{Version: 3, Name: "003_three.sql", Class: migrationOnlineSafe},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validateMigrationCatalog(tt.fs, tt.catalog); err == nil {
				t.Fatal("validateMigrationCatalog() succeeded, want fail-closed error")
			}
		})
	}
}

func TestMigrationPolicyOnlineRefusesAllPendingBeforeExecutingAny(t *testing.T) {
	db := newMigrationPolicyDB(t, true)
	defer db.Close()

	err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationApplyOnlineSafe)
	if !errors.Is(err, ErrOfflineMigrationRequired) {
		t.Fatalf("runMigrations() error = %v, want ErrOfflineMigrationRequired", err)
	}
	var required *OfflineMigrationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("runMigrations() error type = %T, want *OfflineMigrationRequiredError", err)
	}
	if required.CurrentVersion != 1 || required.PendingVersion != 3 || required.PendingName != "003_offline.sql" {
		t.Fatalf("offline migration details = %#v", required)
	}
	assertMigrationPolicyState(t, db, 1, false, false)
}

func TestMigrationPolicyOfflineAppliesPendingInVersionOrder(t *testing.T) {
	db := newMigrationPolicyDB(t, true)
	defer db.Close()

	if err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationApplyOffline); err != nil {
		t.Fatalf("runMigrations() error = %v", err)
	}
	assertMigrationPolicyState(t, db, 3, true, true)
}

func TestMigrationPolicyFreshDatabaseMayApplyFullCatalog(t *testing.T) {
	db := newMigrationPolicyDB(t, false)
	defer db.Close()

	if err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationApplyOnlineSafe); err != nil {
		t.Fatalf("runMigrations() error = %v", err)
	}
	assertMigrationPolicyState(t, db, 3, true, true)
}

func TestMigrationPolicyExistingDatabaseWithNoBusinessRowsIsNotFresh(t *testing.T) {
	db := newMigrationPolicyDB(t, true)
	defer db.Close()

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM one`).Scan(&rows); err != nil {
		t.Fatalf("count empty business rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("business rows = %d, want empty fixture", rows)
	}
	err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationApplyOnlineSafe)
	if !errors.Is(err, ErrOfflineMigrationRequired) {
		t.Fatalf("runMigrations() error = %v, want ErrOfflineMigrationRequired", err)
	}
	assertMigrationPolicyState(t, db, 1, false, false)
}

func TestMigrationPolicyValidateOnlyDoesNotExecutePending(t *testing.T) {
	db := newMigrationPolicyDB(t, true)
	defer db.Close()

	if err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationValidateOnly); err != nil {
		t.Fatalf("runMigrations() error = %v", err)
	}
	assertMigrationPolicyState(t, db, 1, false, false)
}

func TestMigrationPolicyValidateOnlyRejectsFreshDatabase(t *testing.T) {
	db := newMigrationPolicyDB(t, false)
	defer db.Close()

	if err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationValidateOnly); err == nil {
		t.Fatal("runMigrations() validate-only accepted a fresh database")
	}
}

func TestMigrationPolicyRejectsInvalidHistoryAndEmbeddedDrift(t *testing.T) {
	t.Run("historical name drift", func(t *testing.T) {
		db := newMigrationPolicyDB(t, true)
		defer db.Close()
		if _, err := db.Exec(`UPDATE schema_migrations SET name = '001_wrong.sql' WHERE version = 1`); err != nil {
			t.Fatalf("corrupt migration name: %v", err)
		}
		if err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationValidateOnly); err == nil {
			t.Fatal("runMigrations() accepted historical name drift")
		}
	})

	t.Run("future database version", func(t *testing.T) {
		db := newMigrationPolicyDB(t, true)
		defer db.Close()
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, name) VALUES (99, '099_future.sql')`); err != nil {
			t.Fatalf("seed future migration: %v", err)
		}
		if err := runMigrations(context.Background(), db, policyMigrationFS(), policyMigrationCatalog(), migrationValidateOnly); err == nil {
			t.Fatal("runMigrations() accepted a future database version")
		}
	})

	t.Run("embedded SQL file drift", func(t *testing.T) {
		drifted := policyMigrationFS()
		drifted["migrations/004_unregistered.sql"] = &fstest.MapFile{Data: []byte(`SELECT 1`)}
		db := newMigrationPolicyDB(t, true)
		defer db.Close()
		if err := runMigrations(context.Background(), db, drifted, policyMigrationCatalog(), migrationValidateOnly); err == nil {
			t.Fatal("runMigrations() accepted an unregistered embedded SQL file")
		}
	})
}

func policyMigrationFS() fstest.MapFS {
	return fstest.MapFS{
		"migrations/001_one.sql":     {Data: []byte(`CREATE TABLE one (id INTEGER PRIMARY KEY)`)},
		"migrations/002_online.sql":  {Data: []byte(`CREATE TABLE online_probe (id INTEGER PRIMARY KEY)`)},
		"migrations/003_offline.sql": {Data: []byte(`CREATE TABLE offline_probe AS SELECT id FROM online_probe`)},
	}
}

func policyMigrationCatalog() []migrationDescriptor {
	return []migrationDescriptor{
		{Version: 1, Name: "001_one.sql", Class: migrationOnlineSafe},
		{Version: 2, Name: "002_online.sql", Class: migrationOnlineSafe},
		{Version: 3, Name: "003_offline.sql", Class: migrationOfflineRequired},
	}
}

func newMigrationPolicyDB(t *testing.T, existing bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if !existing {
		return db
	}
	if _, err := db.Exec(`
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE one (id INTEGER PRIMARY KEY);
INSERT INTO schema_migrations(version, name) VALUES (1, '001_one.sql');
`); err != nil {
		db.Close()
		t.Fatalf("seed existing database: %v", err)
	}
	return db
}

func assertMigrationPolicyState(t *testing.T, db *sql.DB, wantVersion int, wantOnline, wantOffline bool) {
	t.Helper()
	var version, migrations int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0), COUNT(*) FROM schema_migrations`).Scan(&version, &migrations); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != wantVersion {
		t.Fatalf("schema version = %d, want %d", version, wantVersion)
	}
	if migrations != wantVersion {
		t.Fatalf("migration row count = %d, want %d", migrations, wantVersion)
	}
	for table, want := range map[string]bool{"online_probe": wantOnline, "offline_probe": wantOffline} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if got := count == 1; got != want {
			t.Fatalf("table %s exists = %v, want %v", table, got, want)
		}
	}
}
