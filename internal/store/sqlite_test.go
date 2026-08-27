package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"
)

func seedSQLiteAtMigration(t *testing.T, path string, through int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil || version > through {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", entry.Name(), err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations(version, name) VALUES (?, ?)", version, entry.Name()); err != nil {
			t.Fatalf("record migration %s: %v", entry.Name(), err)
		}
	}
}

func TestOpenSQLiteRejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP); INSERT INTO schema_migrations(version, name) VALUES (99, '099_future.sql')`); err != nil {
		db.Close()
		t.Fatalf("seed future migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	if _, err := OpenSQLite(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second}); err == nil {
		t.Fatal("OpenSQLite() accepted a future schema")
	}
}

func TestOpenSQLiteRejectsMigrationNameMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP); INSERT INTO schema_migrations(version, name) VALUES (1, '001_wrong.sql')`); err != nil {
		db.Close()
		t.Fatalf("seed mismatched migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	if _, err := OpenSQLite(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second}); err == nil {
		t.Fatal("OpenSQLite() accepted a migration name mismatch")
	} else if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
}

func TestOpenSQLiteRejectsExistingDatabaseWithoutMigrationHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unmanaged-schema.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE existing_business_table (id INTEGER PRIMARY KEY)`); err != nil {
		db.Close()
		t.Fatalf("seed existing schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	if _, err := OpenSQLite(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second}); err == nil {
		t.Fatal("OpenSQLite() accepted an existing schema without migration history")
	}
}

func TestOpenSQLitePreflightsOfflineMigrationBeforeConfiguringLiveDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offline-preflight.db")
	testFS := fstest.MapFS{
		"migrations/001_one.sql":     {Data: []byte(`CREATE TABLE one (id INTEGER PRIMARY KEY)`)},
		"migrations/002_online.sql":  {Data: []byte(`CREATE TABLE online_probe (id INTEGER PRIMARY KEY)`)},
		"migrations/003_offline.sql": {Data: []byte(`CREATE TABLE offline_probe (id INTEGER PRIMARY KEY)`)},
	}
	catalog := []migrationDescriptor{
		{Version: 1, Name: "001_one.sql", Class: migrationOnlineSafe},
		{Version: 2, Name: "002_online.sql", Class: migrationOnlineSafe},
		{Version: 3, Name: "003_offline.sql", Class: migrationOfflineRequired},
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(`
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE one (id INTEGER PRIMARY KEY);
INSERT INTO schema_migrations(version, name) VALUES (1, '001_one.sql');
`); err != nil {
		raw.Close()
		t.Fatalf("seed existing database: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	_, err = openSQLiteWithMigrationSet(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second}, testFS, catalog, migrationApplyOnlineSafe)
	if !errors.Is(err, ErrOfflineMigrationRequired) {
		t.Fatalf("openSQLiteWithMigrationSet() error = %v, want ErrOfflineMigrationRequired", err)
	}

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw sqlite: %v", err)
	}
	defer raw.Close()
	var journalMode string
	if err := raw.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal mode: %v", err)
	}
	if journalMode != "delete" {
		t.Fatalf("journal mode = %q, want delete; configure ran before offline preflight", journalMode)
	}
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("WAL sidecar exists before migration authorization, stat error = %v", err)
	}
	for _, table := range []string{"online_probe", "offline_probe"} {
		var count int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("table %s exists before offline preflight approval", table)
		}
	}
}

func TestOpenSQLiteValidatedReadonlyDoesNotApplyPendingMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validated-readonly.db")
	seedSQLiteAtMigration(t, path, 11)

	db, err := OpenSQLiteValidatedReadonly(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteValidatedReadonly() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close validated readonly database: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw sqlite: %v", err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upload_sessions') WHERE name = 'expected_target_identity'`).Scan(&count); err != nil {
		t.Fatalf("query pending migration column: %v", err)
	}
	if count != 0 {
		t.Fatalf("pending migration column count = %d, want 0", count)
	}
	var version int
	if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema version: %v", err)
	}
	if version != 11 {
		t.Fatalf("schema version = %d, want 11", version)
	}
}

func TestOpenSQLiteValidatedReadonlyRejectsFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh-readonly.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("create empty sqlite file: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close empty sqlite file: %v", err)
	}

	if _, err := OpenSQLiteValidatedReadonly(context.Background(), path); err == nil {
		t.Fatal("OpenSQLiteValidatedReadonly() accepted a fresh database")
	}
}

func TestPackagePrivateOfflineMigrationOpenerAppliesPendingMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offline-staging.db")
	seedSQLiteAtMigration(t, path, 11)

	db, err := openSQLiteForOfflineMigration(context.Background(), SQLiteOptions{Path: path, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("openSQLiteForOfflineMigration() error = %v", err)
	}
	defer db.Close()
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upload_sessions') WHERE name = 'expected_target_identity'`).Scan(&count); err != nil {
		t.Fatalf("query migrated column: %v", err)
	}
	if count != 1 {
		t.Fatalf("migrated column count = %d, want 1", count)
	}
	var name string
	if err := db.SQL().QueryRow(`SELECT name FROM schema_migrations WHERE version = 12`).Scan(&name); err != nil {
		t.Fatalf("query migration 012: %v", err)
	}
	if name != "012_upload_target_identity.sql" {
		t.Fatalf("migration 012 name = %q", name)
	}
}

func TestApplyOfflineMigrationsToStagingRejectsLiveAndWrongDirectory(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "omnora.db")
	if err := os.WriteFile(live, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, staging := range []string{
		live,
		filepath.Join(t.TempDir(), "omnora.db.offline-migration-test.db"),
		filepath.Join(root, "arbitrary.db"),
	} {
		if _, err := ApplyOfflineMigrationsToStaging(context.Background(), live, staging); err == nil {
			t.Fatalf("ApplyOfflineMigrationsToStaging(%q) succeeded", staging)
		}
	}
}

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

	for _, table := range []string{"accounts", "mounts", "personal_directories", "mount_grants", "folder_collaborations", "audit_events", "route_groups", "mcp_confirmations", "mcp_transfer_tickets"} {
		var count int
		err := db.SQL().QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count)
		if err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s exists count = %d", table, count)
		}
	}

	for table, columns := range map[string][]string{
		"mcp_confirmations": {
			"id", "public_id", "secret_hash", "account_id", "ai_token_id", "tool_name", "args_hash",
			"object_fingerprint", "impact_json", "status", "created_at", "expires_at", "consumed_at",
		},
		"mcp_transfer_tickets": {
			"id", "public_id", "secret_hash", "account_id", "ai_token_id", "operation", "required_scope",
			"mount_id", "relative_path", "object_fingerprint", "upload_id", "max_bytes",
			"consumed_bytes", "status", "created_at", "expires_at", "closed_at",
		},
	} {
		for _, column := range columns {
			var count int
			err := db.SQL().QueryRow("SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count)
			if err != nil {
				t.Fatalf("query column %s.%s: %v", table, column, err)
			}
			if count != 1 {
				t.Fatalf("column %s.%s exists count = %d", table, column, count)
			}
		}
		var primaryKey int
		if err := db.SQL().QueryRow("SELECT pk FROM pragma_table_info(?) WHERE name = 'id'", table).Scan(&primaryKey); err != nil {
			t.Fatalf("query primary key %s.id: %v", table, err)
		}
		if primaryKey != 1 {
			t.Fatalf("column %s.id pk = %d, want 1", table, primaryKey)
		}
	}

	for _, removed := range []string{"spaces", "space_members"} {
		var count int
		if err := db.SQL().QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?", removed).Scan(&count); err != nil {
			t.Fatalf("query removed table %s: %v", removed, err)
		}
		if count != 0 {
			t.Fatalf("removed table %s still exists", removed)
		}
	}
	var spaceColumns int
	if err := db.SQL().QueryRow(`
SELECT COUNT(*)
FROM sqlite_schema AS schema_object, pragma_table_info(schema_object.name) AS column_info
WHERE schema_object.type = 'table' AND column_info.name LIKE '%space_id%'
`).Scan(&spaceColumns); err != nil {
		t.Fatalf("query removed space_id columns: %v", err)
	}
	if spaceColumns != 0 {
		t.Fatalf("space_id column count = %d, want 0", spaceColumns)
	}

	for _, index := range []struct {
		name string
	}{
		{name: "mcp_confirmations_expiry_status_idx"},
		{name: "mcp_transfer_tickets_expiry_status_idx"},
	} {
		var count int
		if err := db.SQL().QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?", index.name).Scan(&count); err != nil {
			t.Fatalf("query index %s: %v", index.name, err)
		}
		if count != 1 {
			t.Fatalf("index %s exists count = %d", index.name, count)
		}
		rows, err := db.SQL().Query("SELECT seqno, name FROM pragma_index_info(?) ORDER BY seqno", index.name)
		if err != nil {
			t.Fatalf("query index columns %s: %v", index.name, err)
		}
		var columns []string
		for rows.Next() {
			var seqno int
			var column string
			if err := rows.Scan(&seqno, &column); err != nil {
				rows.Close()
				t.Fatalf("scan index columns %s: %v", index.name, err)
			}
			columns = append(columns, column)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate index columns %s: %v", index.name, err)
		}
		rows.Close()
		if len(columns) != 2 || columns[0] != "expires_at" || columns[1] != "status" {
			t.Fatalf("index %s columns = %v, want [expires_at status]", index.name, columns)
		}
	}

	for _, column := range []string{"credential_public_id", "tool_name", "result", "request_id", "trace_id", "parent_event_id"} {
		var count int
		if err := db.SQL().QueryRow("SELECT COUNT(1) FROM pragma_table_info('audit_events') WHERE name = ?", column).Scan(&count); err != nil {
			t.Fatalf("query audit_events column %s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("audit_events column %s exists count = %d", column, count)
		}
	}
}
