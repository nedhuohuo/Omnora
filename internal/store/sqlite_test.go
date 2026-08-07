package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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

	for _, table := range []string{"accounts", "spaces", "mounts", "audit_events", "route_groups", "mcp_confirmations", "mcp_transfer_tickets"} {
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
			"space_id", "mount_id", "relative_path", "object_fingerprint", "upload_id", "max_bytes",
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
