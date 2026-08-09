package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestValidateDatabaseChecksIntegrityForeignKeysMigrationAndInvariants(t *testing.T) {
	path := createValidationDatabase(t, true)
	called := false
	err := ValidateDatabase(context.Background(), path, MigrationTarget{Version: 13, Name: "013_account_mount.sql"}, []DomainInvariant{{
		Name: "account mount ownership",
		Check: func(ctx context.Context, db *sql.DB) error {
			called = true
			var count int
			return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM parent`).Scan(&count)
		},
	}})
	if err != nil {
		t.Fatalf("validate database: %v", err)
	}
	if !called {
		t.Fatal("domain invariant was not called")
	}
}

func TestValidateDatabaseRejectsForeignKeyViolationWithoutRowDetails(t *testing.T) {
	path := createValidationDatabase(t, false)
	err := ValidateDatabase(context.Background(), path, MigrationTarget{Version: 13, Name: "013_account_mount.sql"}, nil)
	if !errors.Is(err, ErrForeignKeyCheck) {
		t.Fatalf("validation error = %v, want ErrForeignKeyCheck", err)
	}
	if strings.Contains(err.Error(), "missing-parent-secret") || strings.Contains(err.Error(), "child") {
		t.Fatalf("foreign key error leaked row details: %v", err)
	}
}

func TestValidateDatabaseRequiresExactTargetMigration(t *testing.T) {
	path := createValidationDatabase(t, true)
	for _, target := range []MigrationTarget{
		{Version: 12, Name: "012_upload_target_identity.sql"},
		{Version: 13, Name: "013_wrong.sql"},
		{Version: 14, Name: "014_future.sql"},
	} {
		err := ValidateDatabase(context.Background(), path, target, nil)
		if !errors.Is(err, ErrMigrationMismatch) {
			t.Fatalf("target %#v error = %v, want ErrMigrationMismatch", target, err)
		}
	}
}

func TestValidateDatabaseDoesNotLeakInvariantErrorDetails(t *testing.T) {
	path := createValidationDatabase(t, true)
	err := ValidateDatabase(context.Background(), path, MigrationTarget{Version: 13, Name: "013_account_mount.sql"}, []DomainInvariant{{
		Name: "one personal directory",
		Check: func(context.Context, *sql.DB) error {
			return errors.New("account row secret@example.com has path /srv/private/alice")
		},
	}})
	if !errors.Is(err, ErrDomainInvariant) {
		t.Fatalf("validation error = %v, want ErrDomainInvariant", err)
	}
	if !strings.Contains(err.Error(), "one personal directory") {
		t.Fatalf("validation error omitted invariant name: %v", err)
	}
	if strings.Contains(err.Error(), "secret@example.com") || strings.Contains(err.Error(), "/srv/private/alice") {
		t.Fatalf("validation error leaked row details: %v", err)
	}
}

func TestValidateDatabaseRejectsCorruptSQLiteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("not a sqlite database; private row data"), 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}
	err := ValidateDatabase(context.Background(), path, MigrationTarget{Version: 13, Name: "013_account_mount.sql"}, nil)
	if !errors.Is(err, ErrIntegrityCheck) {
		t.Fatalf("validation error = %v, want ErrIntegrityCheck", err)
	}
	if strings.Contains(err.Error(), "private row data") {
		t.Fatalf("integrity error leaked database content: %v", err)
	}
}

func createValidationDatabase(t *testing.T, validForeignKey bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "validation.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
PRAGMA foreign_keys = OFF;
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO schema_migrations(version, name) VALUES (12, '012_upload_target_identity.sql');
INSERT INTO schema_migrations(version, name) VALUES (13, '013_account_mount.sql');
CREATE TABLE parent(id TEXT PRIMARY KEY);
CREATE TABLE child(id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent(id));
INSERT INTO parent(id) VALUES ('parent-1');
`); err != nil {
		t.Fatalf("create validation schema: %v", err)
	}
	parentID := "parent-1"
	if !validForeignKey {
		parentID = "missing-parent-secret"
	}
	if _, err := db.Exec(`INSERT INTO child(id, parent_id) VALUES ('child-1', ?)`, parentID); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	return path
}
