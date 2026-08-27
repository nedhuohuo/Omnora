package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// MigrationInfo is the non-sensitive identity of one schema migration.
type MigrationInfo struct {
	Version int
	Name    string
}

func OpenSQLite(ctx context.Context, opts SQLiteOptions) (*DB, error) {
	return openSQLiteWithMigrationSet(ctx, opts, migrationFiles, productionMigrationCatalog, migrationApplyOnlineSafe)
}

// openSQLiteForOfflineMigration applies every explicitly cataloged migration.
// It is package-private until the offline coordinator can enforce that the
// target is an automatically generated staging copy rather than the live DB.
func openSQLiteForOfflineMigration(ctx context.Context, opts SQLiteOptions) (*DB, error) {
	return openSQLiteWithMigrationSet(ctx, opts, migrationFiles, productionMigrationCatalog, migrationApplyOffline)
}

// InspectOfflineMigration reports whether an existing, catalog-validated
// database has an offline-required migration pending. When it does, target is
// the exact latest migration the staging copy will reach; no migration runs.
func InspectOfflineMigration(ctx context.Context, db *DB) (source, target MigrationInfo, pending bool, err error) {
	if db == nil || db.sql == nil {
		return MigrationInfo{}, MigrationInfo{}, false, fmt.Errorf("sqlite database is required")
	}
	plan, err := planMigrations(ctx, db.sql, migrationFiles, productionMigrationCatalog, migrationApplyOffline)
	if err != nil {
		return MigrationInfo{}, MigrationInfo{}, false, err
	}
	if plan.Fresh {
		return MigrationInfo{}, MigrationInfo{}, false, fmt.Errorf("offline migration requires an existing database")
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&source.Version, &source.Name); err != nil {
		return MigrationInfo{}, MigrationInfo{}, false, err
	}
	for _, descriptor := range plan.Pending {
		if descriptor.Class == migrationOfflineRequired {
			latest := productionMigrationCatalog[len(productionMigrationCatalog)-1]
			return source, MigrationInfo{Version: latest.Version, Name: latest.Name}, true, nil
		}
	}
	return source, MigrationInfo{}, false, nil
}

// ApplyOfflineMigrationsToStaging is the only exported offline apply entry.
// It refuses the live path and any target that is not an automatically named
// regular staging file in the live database directory.
func ApplyOfflineMigrationsToStaging(ctx context.Context, livePath, stagingPath string) (*DB, error) {
	liveAbs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(livePath)))
	if err != nil || strings.TrimSpace(livePath) == "" {
		return nil, fmt.Errorf("live sqlite path is required")
	}
	stagingAbs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(stagingPath)))
	if err != nil || strings.TrimSpace(stagingPath) == "" {
		return nil, fmt.Errorf("staging sqlite path is required")
	}
	if liveAbs == stagingAbs || filepath.Dir(liveAbs) != filepath.Dir(stagingAbs) {
		return nil, fmt.Errorf("offline migration staging database must be distinct and in the live database directory")
	}
	prefix := filepath.Base(liveAbs) + ".offline-migration-"
	if !strings.HasPrefix(filepath.Base(stagingAbs), prefix) || !strings.HasSuffix(filepath.Base(stagingAbs), ".db") {
		return nil, fmt.Errorf("offline migration staging database name is invalid")
	}
	for label, path := range map[string]string{"live": liveAbs, "staging": stagingAbs} {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, fmt.Errorf("inspect %s sqlite database: %w", label, statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s sqlite database must be a real regular file", label)
		}
	}
	return openSQLiteForOfflineMigration(ctx, SQLiteOptions{Path: stagingAbs})
}

func openSQLiteWithMigrationSet(ctx context.Context, opts SQLiteOptions, files fs.FS, catalog []migrationDescriptor, policy migrationPolicy) (*DB, error) {
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
	plan, err := planMigrations(ctx, wrapped.sql, files, catalog, policy)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := wrapped.configure(ctx, opts.BusyTimeout); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrationPlan(ctx, wrapped.sql, files, plan); err != nil {
		_ = db.Close()
		return nil, err
	}
	return wrapped, nil
}

// OpenSQLiteValidatedReadonly opens an existing database without executing any
// pending migration and validates its recorded history against the catalog.
func OpenSQLiteValidatedReadonly(ctx context.Context, path string) (*DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	sqlDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", path))
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	wrapper := &DB{sql: sqlDB}
	if err := wrapper.Ping(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := runMigrations(ctx, sqlDB, migrationFiles, productionMigrationCatalog, migrationValidateOnly); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return wrapper, nil
}

// OpenSQLiteValidatedForOfflineSource opens the live database read-write but
// neither configures it nor runs migrations. It exists so the lock-holding
// offline coordinator can take a rollback snapshot and checkpoint WAL before
// atomic replacement.
func OpenSQLiteValidatedForOfflineSource(ctx context.Context, path string) (*DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("sqlite path must be a real regular file")
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	wrapper := &DB{sql: sqlDB}
	if err := wrapper.Ping(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := runMigrations(ctx, sqlDB, migrationFiles, productionMigrationCatalog, migrationValidateOnly); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return wrapper, nil
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
