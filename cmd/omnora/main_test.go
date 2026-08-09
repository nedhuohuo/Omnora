package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/offlinemigration"
)

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
