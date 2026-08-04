package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOnlineBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := OpenSQLite(ctx, SQLiteOptions{Path: filepath.Join(dir, "live.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	defer db.Close()

	if _, err := db.SQL().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS probe(id TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO probe(id, value) VALUES ('a', 'one')`); err != nil {
		t.Fatalf("insert probe row: %v", err)
	}

	backupPath := filepath.Join(dir, "snapshot.db")
	if err := db.BackupTo(ctx, backupPath); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	readonly, err := OpenSQLiteReadonly(ctx, backupPath)
	if err != nil {
		t.Fatalf("OpenSQLiteReadonly: %v", err)
	}
	if err := readonly.IntegrityCheck(ctx); err != nil {
		t.Fatalf("backup integrity: %v", err)
	}
	readonly.Close()

	if _, err := db.SQL().ExecContext(ctx, `UPDATE probe SET value = 'two' WHERE id = 'a'`); err != nil {
		t.Fatalf("mutate live db: %v", err)
	}
	if err := db.RestoreFrom(ctx, backupPath); err != nil {
		t.Fatalf("RestoreFrom: %v", err)
	}
	if err := db.IntegrityCheck(ctx); err != nil {
		t.Fatalf("restored integrity: %v", err)
	}
	var value string
	if err := db.SQL().QueryRowContext(ctx, `SELECT value FROM probe WHERE id = 'a'`).Scan(&value); err != nil {
		t.Fatalf("query restored value: %v", err)
	}
	if value != "one" {
		t.Fatalf("restored value = %q, want %q", value, "one")
	}
}
