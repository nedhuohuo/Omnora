package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProvisionTargetPersonalDirectoriesCreatesAndCanRollbackOnlyNewDirectories(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "staging.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE accounts(id TEXT PRIMARY KEY, status TEXT NOT NULL);
CREATE TABLE personal_directories(account_id TEXT PRIMARY KEY, relative_path TEXT NOT NULL, state TEXT NOT NULL);
INSERT INTO accounts(id, status) VALUES ('acct-active', 'active'), ('acct-retained', 'deleted');
INSERT INTO personal_directories(account_id, relative_path, state) VALUES
  ('acct-active', 'acct-active', 'ready'), ('acct-retained', 'acct-retained', 'retained');
`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	managed := t.TempDir()
	preexisting := filepath.Join(managed, "personal", "acct-retained")
	if err := os.MkdirAll(preexisting, 0o700); err != nil {
		t.Fatal(err)
	}

	cleanup, err := provisionTargetPersonalDirectories(context.Background(), dbPath, managed)
	if err != nil {
		t.Fatalf("provisionTargetPersonalDirectories() error = %v", err)
	}
	created := filepath.Join(managed, "personal", "acct-active")
	if info, err := os.Lstat(created); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("created personal directory: info=%v error=%v", info, err)
	}
	cleanup()
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup left new directory: %v", err)
	}
	if info, err := os.Stat(preexisting); err != nil || !info.IsDir() {
		t.Fatalf("cleanup removed pre-existing directory: info=%v error=%v", info, err)
	}
}

func TestProvisionTargetPersonalDirectoriesRejectsUnsafeBinding(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unsafe.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE accounts(id TEXT PRIMARY KEY, status TEXT NOT NULL);
CREATE TABLE personal_directories(account_id TEXT PRIMARY KEY, relative_path TEXT NOT NULL, state TEXT NOT NULL);
INSERT INTO accounts(id, status) VALUES ('acct-safe', 'active');
INSERT INTO personal_directories(account_id, relative_path, state) VALUES ('acct-safe', '../escape', 'ready');
`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := provisionTargetPersonalDirectories(context.Background(), dbPath, t.TempDir()); err == nil {
		t.Fatal("unsafe personal-directory binding was accepted")
	}
}
