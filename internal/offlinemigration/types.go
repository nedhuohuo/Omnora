package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Phase string

const (
	PhasePreparing           Phase = "preparing"
	PhaseRollbackReady       Phase = "rollback_ready"
	PhaseStagingMigrated     Phase = "staging_migrated"
	PhaseValidated           Phase = "validated"
	PhaseCommitStarted       Phase = "commit_started"
	PhaseCommitIndeterminate Phase = "commit_indeterminate"
	PhaseCompleted           Phase = "completed"
)

func (p Phase) Valid() bool {
	switch p {
	case PhasePreparing, PhaseRollbackReady, PhaseStagingMigrated, PhaseValidated,
		PhaseCommitStarted, PhaseCommitIndeterminate, PhaseCompleted:
		return true
	default:
		return false
	}
}

func (p Phase) Finished() bool {
	return p == PhaseCompleted
}

type Journal struct {
	FormatVersion       int       `json:"format_version"`
	MigrationName       string    `json:"migration_name"`
	Phase               Phase     `json:"phase"`
	SourceSchemaVersion int       `json:"source_schema_version"`
	TargetSchemaVersion int       `json:"target_schema_version"`
	TargetMigrationName string    `json:"target_migration_name,omitempty"`
	RollbackBundlePath  string    `json:"rollback_bundle_path,omitempty"`
	StagingPath         string    `json:"staging_path,omitempty"`
	StartedAt           time.Time `json:"started_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type MigrationTarget struct {
	Version int
	Name    string
}

type DomainInvariant struct {
	Name  string
	Check func(context.Context, *sql.DB) error
}

var (
	ErrLockHeld               = errors.New("offline migration lock is held")
	ErrInvalidPhaseTransition = errors.New("invalid offline migration phase transition")
	ErrCorruptJournal         = errors.New("offline migration journal is corrupt")
	ErrUnfinishedJournal      = errors.New("offline migration is unfinished")
	ErrIntegrityCheck         = errors.New("database integrity check failed")
	ErrForeignKeyCheck        = errors.New("database foreign key check failed")
	ErrMigrationMismatch      = errors.New("database migration target mismatch")
	ErrDomainInvariant        = errors.New("database domain invariant failed")
)
