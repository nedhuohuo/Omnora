package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
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

	open := func() (*DB, error) {
		db, err := sql.Open("sqlite", opts.Path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)

		wrapped := &DB{sql: db}
		if err := wrapped.configure(ctx, opts.BusyTimeout); err != nil {
			_ = db.Close()
			return nil, err
		}
		return wrapped, nil
	}

	wrapped, err := open()
	if err != nil {
		return nil, err
	}
	if reason, err := wrapped.incompatibleSchemaReason(ctx); err != nil {
		_ = wrapped.Close()
		return nil, err
	} else if reason != "" {
		backupPath, err := wrapped.backupIncompatibleDatabase(ctx, opts.Path)
		if err != nil {
			return nil, err
		}
		slog.Warn("incompatible sqlite schema backed up; creating a fresh database", "reason", reason, "backup_path", backupPath)
		wrapped, err = open()
		if err != nil {
			return nil, fmt.Errorf("open fresh sqlite database after backing up %s: %w", backupPath, err)
		}
	}
	if err := wrapped.migrate(ctx); err != nil {
		_ = wrapped.Close()
		return nil, err
	}
	if reason, err := wrapped.incompatibleSchemaReason(ctx); err != nil {
		_ = wrapped.Close()
		return nil, err
	} else if reason != "" {
		_ = wrapped.Close()
		return nil, fmt.Errorf("sqlite schema is incomplete after migration: %s", reason)
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

var compatibilityTables = []string{"spaces", "mounts", "catalog_entries", "jobs"}

var compatibilityColumns = map[string][]string{
	"mounts":          {"space_id"},
	"catalog_entries": {"space_id"},
}

func (db *DB) incompatibleSchemaReason(ctx context.Context) (string, error) {
	hasMigrations, err := db.tableExists(ctx, "schema_migrations")
	if err != nil {
		return "", err
	}
	if !hasMigrations {
		count, err := db.applicationTableCount(ctx)
		if err != nil {
			return "", err
		}
		if count > 0 {
			return "database has application tables but no migration history", nil
		}
		return "", nil
	}

	var versions int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations`).Scan(&versions); err != nil {
		return "", err
	}
	if versions == 0 {
		count, err := db.applicationTableCount(ctx)
		if err != nil {
			return "", err
		}
		if count > 0 {
			return "database has application tables but empty migration history", nil
		}
		return "", nil
	}

	for _, table := range compatibilityTables {
		exists, err := db.tableExists(ctx, table)
		if err != nil {
			return "", err
		}
		if !exists {
			return "missing required table: " + table, nil
		}
	}
	for table, columns := range compatibilityColumns {
		for _, column := range columns {
			exists, err := db.columnExists(ctx, table, column)
			if err != nil {
				return "", err
			}
			if !exists {
				return "missing required column: " + table + "." + column, nil
			}
		}
	}
	return "", nil
}

func (db *DB) tableExists(ctx context.Context, table string) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
	return count > 0, err
}

func (db *DB) columnExists(ctx context.Context, table, column string) (bool, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count)
	return count > 0, err
}

func (db *DB) applicationTableCount(ctx context.Context) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
  AND name <> 'schema_migrations'
`).Scan(&count)
	return count, err
}

func (db *DB) backupIncompatibleDatabase(ctx context.Context, path string) (string, error) {
	if _, err := db.sql.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		return "", fmt.Errorf("checkpoint incompatible sqlite database: %w", err)
	}
	if err := db.Close(); err != nil {
		return "", fmt.Errorf("close incompatible sqlite database: %w", err)
	}

	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backupPath := path + ".incompatible-" + stamp + ".bak"
	if err := os.Rename(path, backupPath); err != nil {
		return "", fmt.Errorf("back up incompatible sqlite database to %s: %w", backupPath, err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove stale sqlite sidecar %s after backup %s: %w", sidecar, backupPath, err)
		}
	}
	return backupPath, nil
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
