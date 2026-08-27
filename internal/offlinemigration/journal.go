package offlinemigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const journalFormatVersion = 1

func DefaultJournalPath(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return ""
	}
	return dbPath + ".offline-migration.json"
}

func ValidateTransition(from, to Phase) error {
	allowed := false
	switch from {
	case PhasePreparing:
		allowed = to == PhaseRollbackReady
	case PhaseRollbackReady:
		allowed = to == PhaseStagingMigrated
	case PhaseStagingMigrated:
		allowed = to == PhaseValidated
	case PhaseValidated:
		allowed = to == PhaseCommitStarted
	case PhaseCommitStarted:
		allowed = to == PhaseCommitIndeterminate || to == PhaseCompleted
	case PhaseCommitIndeterminate:
		allowed = to == PhaseCompleted
	}
	if !from.Valid() || !to.Valid() || !allowed {
		return fmt.Errorf("%w: %s to %s", ErrInvalidPhaseTransition, from, to)
	}
	return nil
}

// WriteJournal atomically publishes a complete, private journal and makes
// both the file contents and directory entry durable before it returns.
func WriteJournal(path string, journal Journal) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("offline migration journal path is required")
	}
	if journal.FormatVersion == 0 {
		journal.FormatVersion = journalFormatVersion
	}
	if err := validateJournal(journal); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode offline migration journal: %w", err)
	}
	encoded = append(encoded, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create offline migration journal directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create offline migration journal temp file: %w", err)
	}
	tempPath := temp.Name()
	installed := false
	defer func() {
		_ = temp.Close()
		if !installed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect offline migration journal temp file: %w", err)
	}
	if _, err := temp.Write(encoded); err != nil {
		return fmt.Errorf("write offline migration journal temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync offline migration journal temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close offline migration journal temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install offline migration journal: %w", err)
	}
	installed = true
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync offline migration journal directory: %w", err)
	}
	return nil
}

func ReadJournal(path string) (Journal, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Journal{}, errors.New("offline migration journal path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Journal{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("%w: invalid JSON", ErrCorruptJournal)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Journal{}, fmt.Errorf("%w: trailing JSON content", ErrCorruptJournal)
	}
	if err := validateJournal(journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func AdvanceJournal(path string, next Phase, now time.Time, update func(*Journal)) (Journal, error) {
	journal, err := ReadJournal(path)
	if err != nil {
		return Journal{}, err
	}
	if err := ValidateTransition(journal.Phase, next); err != nil {
		return Journal{}, err
	}
	if update != nil {
		update(&journal)
	}
	journal.Phase = next
	journal.UpdatedAt = now.UTC()
	if err := WriteJournal(path, journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func EnsureNoUnfinishedJournal(dbPath string) error {
	path := DefaultJournalPath(dbPath)
	if path == "" {
		return errors.New("offline migration database path is required")
	}
	journal, err := ReadJournal(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !journal.Phase.Finished() {
		return fmt.Errorf("%w: phase %s", ErrUnfinishedJournal, journal.Phase)
	}
	return nil
}

func validateJournal(journal Journal) error {
	if journal.FormatVersion != journalFormatVersion {
		return fmt.Errorf("%w: unsupported format version", ErrCorruptJournal)
	}
	if strings.TrimSpace(journal.MigrationName) == "" {
		return fmt.Errorf("%w: migration name is missing", ErrCorruptJournal)
	}
	if !journal.Phase.Valid() {
		return fmt.Errorf("%w: phase is invalid", ErrCorruptJournal)
	}
	if journal.SourceSchemaVersion < 0 || journal.TargetSchemaVersion <= journal.SourceSchemaVersion {
		return fmt.Errorf("%w: schema versions are invalid", ErrCorruptJournal)
	}
	if journal.StartedAt.IsZero() || journal.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: timestamps are missing", ErrCorruptJournal)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
