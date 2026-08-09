package offlinemigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalAtomicWriteReadAndPrivateMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	path := DefaultJournalPath(dbPath)
	started := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	journal := Journal{
		MigrationName:       "account_mount",
		Phase:               PhasePreparing,
		SourceSchemaVersion: 12,
		TargetSchemaVersion: 13,
		StartedAt:           started,
		UpdatedAt:           started,
	}
	if err := WriteJournal(path, journal); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	journal.Phase = PhaseRollbackReady
	journal.UpdatedAt = started.Add(time.Minute)
	if err := WriteJournal(path, journal); err != nil {
		t.Fatalf("replace journal: %v", err)
	}
	got, err := ReadJournal(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if got.Phase != PhaseRollbackReady || !got.UpdatedAt.Equal(journal.UpdatedAt) {
		t.Fatalf("journal = %#v, want replacement contents", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("journal mode = %04o, want 0600", gotMode)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read journal directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("atomic journal write left temp file %q", entry.Name())
		}
	}
}

func TestJournalTransitionsRejectSkipsAndFallbacks(t *testing.T) {
	legal := []Phase{
		PhasePreparing,
		PhaseRollbackReady,
		PhaseStagingMigrated,
		PhaseValidated,
		PhaseCommitStarted,
		PhaseCompleted,
	}
	for i := 1; i < len(legal); i++ {
		if err := ValidateTransition(legal[i-1], legal[i]); err != nil {
			t.Fatalf("transition %s -> %s rejected: %v", legal[i-1], legal[i], err)
		}
	}
	if err := ValidateTransition(PhaseCommitStarted, PhaseCommitIndeterminate); err != nil {
		t.Fatalf("commit indeterminate transition rejected: %v", err)
	}
	if err := ValidateTransition(PhaseCommitIndeterminate, PhaseCompleted); err != nil {
		t.Fatalf("resolved indeterminate transition rejected: %v", err)
	}

	for _, tc := range []struct{ from, to Phase }{
		{PhasePreparing, PhaseValidated},
		{PhaseValidated, PhaseRollbackReady},
		{PhaseCommitStarted, PhaseValidated},
		{PhaseCompleted, PhasePreparing},
	} {
		if err := ValidateTransition(tc.from, tc.to); !errors.Is(err, ErrInvalidPhaseTransition) {
			t.Fatalf("transition %s -> %s error = %v, want ErrInvalidPhaseTransition", tc.from, tc.to, err)
		}
	}
}

func TestJournalRecognizesCorruptAndUnfinishedState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "omnora.db")
	path := DefaultJournalPath(dbPath)
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt journal: %v", err)
	}
	if _, err := ReadJournal(path); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("read corrupt journal error = %v, want ErrCorruptJournal", err)
	}
	if err := EnsureNoUnfinishedJournal(dbPath); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("corrupt journal gate error = %v, want ErrCorruptJournal", err)
	}

	journal := Journal{
		MigrationName:       "account_mount",
		Phase:               PhaseValidated,
		SourceSchemaVersion: 12,
		TargetSchemaVersion: 13,
		StartedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if err := WriteJournal(path, journal); err != nil {
		t.Fatalf("write unfinished journal: %v", err)
	}
	if err := EnsureNoUnfinishedJournal(dbPath); !errors.Is(err, ErrUnfinishedJournal) {
		t.Fatalf("unfinished journal gate error = %v, want ErrUnfinishedJournal", err)
	}

	journal.Phase = PhaseCompleted
	if err := WriteJournal(path, journal); err != nil {
		t.Fatalf("write completed journal: %v", err)
	}
	if err := EnsureNoUnfinishedJournal(dbPath); err != nil {
		t.Fatalf("completed journal blocked startup: %v", err)
	}
}

func TestJournalRejectsMissingRequiredFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := WriteJournal(path, Journal{Phase: PhasePreparing}); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("write invalid journal error = %v, want ErrCorruptJournal", err)
	}
}
