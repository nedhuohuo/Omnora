package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type SQLiteOptions struct {
	Path        string
	BusyTimeout time.Duration
}

type DB struct {
	sql *sql.DB
}

func OpenSQLite(ctx context.Context, opts SQLiteOptions) (*DB, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 5 * time.Second
	}

	db, err := sql.Open("sqlite", opts.Path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	wrapped := &DB{sql: db}
	if err := wrapped.configure(ctx, opts.BusyTimeout); err != nil {
		db.Close()
		return nil, err
	}
	if err := wrapped.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return wrapped, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

func (db *DB) Ping(ctx context.Context) error {
	return db.sql.PingContext(ctx)
}

func (db *DB) SQL() *sql.DB {
	return db.sql
}

func (db *DB) configure(ctx context.Context, busyTimeout time.Duration) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout.Milliseconds()),
	}
	for _, pragma := range pragmas {
		if _, err := db.sql.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return nil
}

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	expectedNames := make(map[int]string, len(entries))
	currentVersion := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		expectedNames[version] = entry.Name()
		if version > currentVersion {
			currentVersion = version
		}
	}

	rows, err := db.sql.QueryContext(ctx, "SELECT version, name FROM schema_migrations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			rows.Close()
			return err
		}
		if version > currentVersion {
			rows.Close()
			return fmt.Errorf("database schema version %d is newer than supported version %d", version, currentVersion)
		}
		if expected, ok := expectedNames[version]; !ok || expected != name {
			rows.Close()
			return fmt.Errorf("database migration %d has name %q, want %q", version, name, expected)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}

		var exists int
		err = db.sql.QueryRowContext(ctx, "SELECT COUNT(1) FROM schema_migrations WHERE version = ?", version).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}

		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name) VALUES (?, ?)", version, entry.Name()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q must start with numeric version", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q: %w", name, err)
	}
	return version, nil
}
