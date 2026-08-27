package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

// ValidateDatabase opens a closed staging database read-only and proves its
// SQLite structure, foreign keys, exact target migration, and domain rules.
// Validation failures intentionally omit query results and row values.
func ValidateDatabase(ctx context.Context, path string, target MigrationTarget, invariants []DomainInvariant) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("database validation path is required")
	}
	if target.Version <= 0 || strings.TrimSpace(target.Name) == "" {
		return fmt.Errorf("%w: expected target is invalid", ErrMigrationMismatch)
	}
	location := &url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", location.String()+"?mode=ro")
	if err != nil {
		return ErrIntegrityCheck
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := validateIntegrity(ctx, db); err != nil {
		return err
	}
	if err := validateForeignKeys(ctx, db); err != nil {
		return err
	}
	if err := validateMigrationTarget(ctx, db, target); err != nil {
		return err
	}
	for _, invariant := range invariants {
		name := strings.TrimSpace(invariant.Name)
		if name == "" || invariant.Check == nil {
			return fmt.Errorf("%w: invalid invariant definition", ErrDomainInvariant)
		}
		if err := invariant.Check(ctx, db); err != nil {
			return fmt.Errorf("%w: %q", ErrDomainInvariant, name)
		}
	}
	return nil
}

func validateIntegrity(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return ErrIntegrityCheck
	}
	defer rows.Close()
	count := 0
	valid := true
	for rows.Next() {
		count++
		var result string
		if err := rows.Scan(&result); err != nil || !strings.EqualFold(strings.TrimSpace(result), "ok") {
			valid = false
		}
	}
	if rows.Err() != nil || count != 1 || !valid {
		return ErrIntegrityCheck
	}
	return nil
}

func validateForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return ErrForeignKeyCheck
	}
	defer rows.Close()
	if rows.Next() {
		return ErrForeignKeyCheck
	}
	if rows.Err() != nil {
		return ErrForeignKeyCheck
	}
	return nil
}

func validateMigrationTarget(ctx context.Context, db *sql.DB, target MigrationTarget) error {
	var version int
	var name string
	if err := db.QueryRowContext(ctx, `
SELECT version, name
FROM schema_migrations
ORDER BY version DESC
LIMIT 1
`).Scan(&version, &name); err != nil {
		return ErrMigrationMismatch
	}
	if version != target.Version || name != target.Name {
		return ErrMigrationMismatch
	}
	return nil
}
