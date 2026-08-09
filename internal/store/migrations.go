package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

type migrationClass string

const (
	migrationOnlineSafe      migrationClass = "online_safe"
	migrationOfflineRequired migrationClass = "offline_required"
)

type migrationPolicy uint8

const (
	migrationValidateOnly migrationPolicy = iota
	migrationApplyOnlineSafe
	migrationApplyOffline
)

type migrationDescriptor struct {
	Version int
	Name    string
	Class   migrationClass
}

var ErrOfflineMigrationRequired = errors.New("offline migration required")

type OfflineMigrationRequiredError struct {
	CurrentVersion int
	PendingVersion int
	PendingName    string
}

func (e *OfflineMigrationRequiredError) Error() string {
	return fmt.Sprintf("%s: database is at version %d; migration %d (%s) requires offline execution",
		ErrOfflineMigrationRequired, e.CurrentVersion, e.PendingVersion, e.PendingName)
}

func (e *OfflineMigrationRequiredError) Is(target error) bool {
	return target == ErrOfflineMigrationRequired
}

var productionMigrationCatalog = []migrationDescriptor{
	{Version: 1, Name: "001_initial_schema.sql", Class: migrationOnlineSafe},
	{Version: 2, Name: "002_control_plane.sql", Class: migrationOnlineSafe},
	{Version: 3, Name: "003_session_entry.sql", Class: migrationOnlineSafe},
	{Version: 4, Name: "004_remove_emergency_access.sql", Class: migrationOnlineSafe},
	{Version: 5, Name: "005_share_fragment_secret.sql", Class: migrationOnlineSafe},
	{Version: 6, Name: "006_standard_mcp.sql", Class: migrationOnlineSafe},
	{Version: 7, Name: "007_identity_security.sql", Class: migrationOnlineSafe},
	{Version: 8, Name: "008_file_operations_and_uploads.sql", Class: migrationOnlineSafe},
	{Version: 9, Name: "009_share_tickets_and_identity.sql", Class: migrationOnlineSafe},
	{Version: 10, Name: "010_catalog_jobs.sql", Class: migrationOnlineSafe},
	{Version: 11, Name: "011_recovery_control.sql", Class: migrationOnlineSafe},
	{Version: 12, Name: "012_upload_target_identity.sql", Class: migrationOnlineSafe},
	{Version: 13, Name: "013_account_mount_model.sql", Class: migrationOfflineRequired},
}

func validateMigrationCatalog(files fs.FS, catalog []migrationDescriptor) ([]migrationDescriptor, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	discoveredByName := make(map[string]int)
	discoveredByVersion := make(map[int]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		if existing, ok := discoveredByVersion[version]; ok {
			return nil, fmt.Errorf("embedded migrations %q and %q use duplicate version %d", existing, entry.Name(), version)
		}
		discoveredByName[entry.Name()] = version
		discoveredByVersion[version] = entry.Name()
	}

	validated := append([]migrationDescriptor(nil), catalog...)
	seenVersions := make(map[int]string, len(validated))
	seenNames := make(map[string]int, len(validated))
	for _, descriptor := range validated {
		if descriptor.Class != migrationOnlineSafe && descriptor.Class != migrationOfflineRequired {
			return nil, fmt.Errorf("migration %q has unknown class %q", descriptor.Name, descriptor.Class)
		}
		if existing, ok := seenVersions[descriptor.Version]; ok {
			return nil, fmt.Errorf("migration descriptors %q and %q use duplicate version %d", existing, descriptor.Name, descriptor.Version)
		}
		if existing, ok := seenNames[descriptor.Name]; ok {
			return nil, fmt.Errorf("migration name %q is registered for duplicate versions %d and %d", descriptor.Name, existing, descriptor.Version)
		}
		fileVersion, ok := discoveredByName[descriptor.Name]
		if !ok {
			return nil, fmt.Errorf("migration descriptor %d %q has no embedded SQL file", descriptor.Version, descriptor.Name)
		}
		if fileVersion != descriptor.Version {
			return nil, fmt.Errorf("migration descriptor %d %q disagrees with filename version %d", descriptor.Version, descriptor.Name, fileVersion)
		}
		seenVersions[descriptor.Version] = descriptor.Name
		seenNames[descriptor.Name] = descriptor.Version
	}
	for name, version := range discoveredByName {
		if registered, ok := seenNames[name]; !ok || registered != version {
			return nil, fmt.Errorf("embedded migration %d %q is not registered exactly once", version, name)
		}
	}
	if len(discoveredByName) != len(validated) {
		return nil, fmt.Errorf("migration catalog has %d descriptors for %d embedded SQL files", len(validated), len(discoveredByName))
	}

	sort.Slice(validated, func(i, j int) bool { return validated[i].Version < validated[j].Version })
	if len(validated) == 0 {
		return nil, errors.New("migration catalog must start at version 1")
	}
	for index, descriptor := range validated {
		want := index + 1
		if descriptor.Version != want {
			return nil, fmt.Errorf("migration catalog version %d is not continuous; want %d", descriptor.Version, want)
		}
	}
	return validated, nil
}

type migrationPlan struct {
	Fresh   bool
	Pending []migrationDescriptor
}

func planMigrations(ctx context.Context, db *sql.DB, files fs.FS, catalog []migrationDescriptor, policy migrationPolicy) (migrationPlan, error) {
	validated, err := validateMigrationCatalog(files, catalog)
	if err != nil {
		return migrationPlan{}, err
	}
	if policy != migrationValidateOnly && policy != migrationApplyOnlineSafe && policy != migrationApplyOffline {
		return migrationPlan{}, fmt.Errorf("unknown migration policy %d", policy)
	}

	fresh, err := databaseIsFresh(ctx, db)
	if err != nil {
		return migrationPlan{}, err
	}
	if fresh && policy == migrationValidateOnly {
		return migrationPlan{}, errors.New("validate-only requires an existing database with migration history")
	}

	applied := make(map[int]string)
	currentVersion := 0
	if !fresh {
		applied, currentVersion, err = readAndValidateMigrationHistory(ctx, db, validated)
		if err != nil {
			return migrationPlan{}, err
		}
	}
	pending := make([]migrationDescriptor, 0, len(validated)-len(applied))
	for _, descriptor := range validated {
		if _, exists := applied[descriptor.Version]; !exists {
			pending = append(pending, descriptor)
		}
	}
	if policy == migrationValidateOnly {
		return migrationPlan{Fresh: fresh}, nil
	}
	if policy == migrationApplyOnlineSafe && !fresh {
		for _, descriptor := range pending {
			if descriptor.Class == migrationOfflineRequired {
				return migrationPlan{}, &OfflineMigrationRequiredError{
					CurrentVersion: currentVersion,
					PendingVersion: descriptor.Version,
					PendingName:    descriptor.Name,
				}
			}
		}
	}
	return migrationPlan{Fresh: fresh, Pending: pending}, nil
}

func applyMigrationPlan(ctx context.Context, db *sql.DB, files fs.FS, plan migrationPlan) error {
	if plan.Fresh {
		if err := createSchemaMigrations(ctx, db); err != nil {
			return err
		}
	}
	for _, descriptor := range plan.Pending {
		body, err := fs.ReadFile(files, "migrations/"+descriptor.Name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", descriptor.Name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name) VALUES (?, ?)", descriptor.Version, descriptor.Name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func runMigrations(ctx context.Context, db *sql.DB, files fs.FS, catalog []migrationDescriptor, policy migrationPolicy) error {
	plan, err := planMigrations(ctx, db, files, catalog, policy)
	if err != nil {
		return err
	}
	if policy == migrationValidateOnly {
		return nil
	}
	return applyMigrationPlan(ctx, db, files, plan)
}

func databaseIsFresh(ctx context.Context, db *sql.DB) (bool, error) {
	var objects int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		return false, err
	}
	return objects == 0, nil
}

func createSchemaMigrations(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`)
	return err
}

func readAndValidateMigrationHistory(ctx context.Context, db *sql.DB, catalog []migrationDescriptor) (map[int]string, int, error) {
	var migrationTable int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&migrationTable); err != nil {
		return nil, 0, err
	}
	if migrationTable != 1 {
		return nil, 0, errors.New("existing database is missing schema_migrations")
	}

	expected := make(map[int]migrationDescriptor, len(catalog))
	for _, descriptor := range catalog {
		expected[descriptor.Version] = descriptor
	}
	rows, err := db.QueryContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	applied := make(map[int]string)
	currentVersion := 0
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, 0, err
		}
		descriptor, ok := expected[version]
		if !ok {
			return nil, 0, fmt.Errorf("database schema version %d is not supported", version)
		}
		if descriptor.Name != name {
			return nil, 0, fmt.Errorf("database migration %d has name %q, want %q", version, name, descriptor.Name)
		}
		applied[version] = name
		if version > currentVersion {
			currentVersion = version
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for _, descriptor := range catalog {
		if descriptor.Version > currentVersion {
			break
		}
		if _, ok := applied[descriptor.Version]; !ok {
			return nil, 0, fmt.Errorf("database migration history is missing version %d before current version %d", descriptor.Version, currentVersion)
		}
	}
	return applied, currentVersion, nil
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
