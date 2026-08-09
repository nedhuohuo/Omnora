package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/store"
)

func TestMigrationCoordinatorEndToEndStagesValidatesAndAtomicallyCommits(t *testing.T) {
	request := newCoordinatorRequest(t)
	coordinator := newInjectedCoordinator(t)

	result, err := coordinator.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.NoPendingMigration || result.Target != (MigrationTarget{Version: 14, Name: "014_injected_test.sql"}) {
		t.Fatalf("Run() result = %#v", result)
	}
	journal, err := ReadJournal(result.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != PhaseCompleted || journal.RollbackBundlePath == "" || journal.StagingPath == "" {
		t.Fatalf("journal = %#v", journal)
	}
	if _, err := os.Stat(filepath.Join(result.RollbackBundlePath, "ROLLBACK_READY")); err != nil {
		t.Fatalf("rollback bundle is not ready: %v", err)
	}
	if err := ValidateDatabase(context.Background(), request.DBPath, result.Target, nil); err != nil {
		t.Fatalf("committed database validation: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(request.DBPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("live SQLite sidecar %s remains: %v", suffix, err)
		}
	}
}

func TestMigrationCoordinatorNoPendingOfflineMigrationIsSafeNoop(t *testing.T) {
	request := newCoordinatorRequest(t)
	coordinator := NewMigrationCoordinator()

	result, err := coordinator.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.NoPendingMigration {
		t.Fatalf("Run() result = %#v, want no pending migration", result)
	}
	if _, err := os.Stat(DefaultJournalPath(request.DBPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op created a migration journal: %v", err)
	}
	if entries, err := os.ReadDir(request.RollbackRoot); err == nil && len(entries) != 0 {
		t.Fatalf("no-op created rollback artifacts: %v", entries)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestMigrationCoordinatorPostCommitFailureIsCommitIndeterminate(t *testing.T) {
	request := newCoordinatorRequest(t)
	coordinator := newInjectedCoordinator(t)
	validations := 0
	coordinator.deps.validateDatabase = func(ctx context.Context, path string, target MigrationTarget, invariants []DomainInvariant) error {
		validations++
		if validations == 2 {
			return errors.New("injected post-commit failure")
		}
		return ValidateDatabase(ctx, path, target, invariants)
	}

	if _, err := coordinator.Run(context.Background(), request); err == nil || !strings.Contains(err.Error(), "post-commit") {
		t.Fatalf("Run() error = %v", err)
	}
	journal, err := ReadJournal(DefaultJournalPath(request.DBPath))
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != PhaseCommitIndeterminate {
		t.Fatalf("journal phase = %s, want %s", journal.Phase, PhaseCommitIndeterminate)
	}
}

func newCoordinatorRequest(t *testing.T) MigrationRequest {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := MigrationRequest{
		DBPath: filepath.Join(root, "data", "omnora.db"), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), ManagedDir: filepath.Join(root, "managed"),
		RollbackRoot: filepath.Join(root, "rollback"),
	}
	for _, dir := range []string{request.ConfigDir, request.DataDir, request.ManagedDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".omnora-instance-id"), []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(request.ManagedDir, "personal.txt"), []byte("preserved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: request.DBPath, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`CREATE TABLE offline_source_probe(id INTEGER PRIMARY KEY)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return request
}

func newInjectedCoordinator(t *testing.T) *MigrationCoordinator {
	t.Helper()
	coordinator := NewMigrationCoordinator()
	coordinator.deps.randomID = func() (string, error) { return "test", nil }
	coordinator.deps.inspect = func(context.Context, *store.DB) (migrationInspection, error) {
		return migrationInspection{
			Source: store.MigrationInfo{Version: 13, Name: "013_account_mount_model.sql"},
			Target: store.MigrationInfo{Version: 14, Name: "014_injected_test.sql"}, Pending: true,
		}, nil
	}
	coordinator.deps.migrateStaging = func(ctx context.Context, _, stagingPath string) error {
		db, err := sql.Open("sqlite", stagingPath)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `
CREATE TABLE account_mount_migration_probe(id INTEGER PRIMARY KEY);
INSERT INTO schema_migrations(version, name) VALUES (14, '014_injected_test.sql');
`); err != nil {
			_ = db.Close()
			return err
		}
		if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			_ = db.Close()
			return err
		}
		if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
			_ = db.Close()
			return err
		}
		return db.Close()
	}
	return coordinator
}
