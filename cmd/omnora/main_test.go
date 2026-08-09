package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/identity"
	"omnora/internal/offlinemigration"
	"omnora/internal/store"
)

func TestPrepareInitializationLogsTokenWhenEnabledAndDatabaseIsUninitialized(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "initialization-log.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	token := "visible-initialization-token"
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err = prepareInitialization(ctx, identity.New(db.SQL(), identity.Options{}), config.InitializationConfig{
		Token: token, TTL: time.Hour, LogToken: true,
	}, logger)
	if err != nil {
		t.Fatalf("prepareInitialization() error = %v", err)
	}
	if strings.Count(output.String(), token) != 1 || !strings.Contains(output.String(), `"initialization_token"`) {
		t.Fatalf("initialization token log = %q", output.String())
	}
}

func TestPrepareInitializationDoesNotLogTokenWhenDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "initialization-log-disabled.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err = prepareInitialization(ctx, identity.New(db.SQL(), identity.Options{}), config.InitializationConfig{
		Token: "must-not-be-logged", TTL: time.Hour, LogToken: false,
	}, logger)
	if err != nil {
		t.Fatalf("prepareInitialization() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("disabled initialization token log = %q, want empty", output.String())
	}
}

func TestPrepareInitializationDoesNotLogTokenAfterInitializationWasConsumed(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "initialization-log-consumed.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := identity.New(db.SQL(), identity.Options{})
	if _, err := svc.PrepareInitializationWithToken(ctx, "consumed-initialization-token", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE identity_initialization SET consumed_at = CURRENT_TIMESTAMP WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err = prepareInitialization(ctx, svc, config.InitializationConfig{
		Token: "consumed-initialization-token", TTL: time.Hour, LogToken: true,
	}, logger)
	if err != nil {
		t.Fatalf("prepareInitialization() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("consumed initialization token log = %q, want empty", output.String())
	}
}

func TestPrepareInitializationDoesNotLogTokenWhenDatabaseCheckFails(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "initialization-log-error.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := identity.New(db.SQL(), identity.Options{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	token := "failed-database-token"
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err = prepareInitialization(ctx, svc, config.InitializationConfig{
		Token: token, TTL: time.Hour, LogToken: true,
	}, logger)
	if err == nil {
		t.Fatal("prepareInitialization() error = nil, want database error")
	}
	if strings.Contains(output.String(), token) {
		t.Fatalf("failed initialization leaked token: %q", output.String())
	}
}

func TestAcquireDatabaseLifecycleHoldsLockUntilClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	lock, err := acquireDatabaseLifecycle(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := offlinemigration.AcquireLock(dbPath); !errors.Is(err, offlinemigration.ErrLockHeld) {
		t.Fatalf("second lock error = %v, want ErrLockHeld", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := offlinemigration.AcquireLock(dbPath)
	if err != nil {
		t.Fatalf("lock after close: %v", err)
	}
	_ = second.Close()
}

func TestAcquireDatabaseLifecycleRejectsUnfinishedJournalBeforeStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	now := time.Now().UTC()
	if err := offlinemigration.WriteJournal(offlinemigration.DefaultJournalPath(dbPath), offlinemigration.Journal{
		MigrationName: "account_mount", Phase: offlinemigration.PhaseValidated,
		SourceSchemaVersion: 12, TargetSchemaVersion: 13, StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireDatabaseLifecycle(dbPath); !errors.Is(err, offlinemigration.ErrUnfinishedJournal) {
		t.Fatalf("startup gate error = %v, want ErrUnfinishedJournal", err)
	}
	lock, err := offlinemigration.AcquireLock(dbPath)
	if err != nil {
		t.Fatalf("failed startup gate leaked lock: %v", err)
	}
	_ = lock.Close()
}
