// Package recovery owns the durable restore/recovery state machine.
//
// The coordinator deliberately does not register HTTP routes. It is safe to
// use from startup/recovery tooling and keeps the database as the source of
// truth when a process must fail closed.
package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type State string

const (
	StateNormal                 State = "normal"
	StatePreparing              State = "preparing"
	StateRecoveryRequired       State = "recovery_required"
	StateRestoring              State = "restoring"
	StateFinalizeRequired       State = "finalize_required"
	StateNormalPendingBootstrap State = "normal_pending_bootstrap"
)

var (
	ErrInvalidRequest       = errors.New("recovery: invalid request")
	ErrRestoreInProgress    = errors.New("recovery: restore already in progress")
	ErrInvalidTransition    = errors.New("recovery: invalid state transition")
	ErrRequestNotFound      = errors.New("recovery: restore request not found")
	ErrRequestMismatch      = errors.New("recovery: active restore request mismatch")
	ErrNotReadyForBootstrap = errors.New("recovery: restore is not ready for bootstrap")
)

type Control struct {
	State               State
	Ready               bool
	RequestID           string
	StagingPath         string
	SourceSchemaVersion sql.NullInt64
	SafeSnapshotPath    string
	ReasonCode          string
	CleanupPending      bool
	RequestedAt         time.Time
	CompletedAt         time.Time
	UpdatedAt           time.Time
}

type RestoreRequest struct {
	ID                  string
	BackupID            string
	State               State
	StagingPath         string
	SourceSchemaVersion sql.NullInt64
	SafeSnapshotPath    string
	ReasonCode          string
	CleanupPending      bool
	RequestedAt         time.Time
	CompletedAt         time.Time
}

type BeginRestoreRequest struct {
	BackupID       string
	ActorAccountID string
	RequestID      string
	StagingPath    string
}

type ArtifactUpdate struct {
	StagingPath         string
	SourceSchemaVersion *int64
	SafeSnapshotPath    string
}

type AuditEvent struct {
	Action         string
	TargetType     string
	TargetID       string
	ActorAccountID string
	MetadataJSON   string
}

type AuditWriter func(context.Context, *sql.Tx, AuditEvent) error

type Option func(*Coordinator)

func WithClock(clock func() time.Time) Option {
	return func(c *Coordinator) {
		if clock != nil {
			c.clock = clock
		}
	}
}

func WithAuditWriter(writer AuditWriter) Option {
	return func(c *Coordinator) {
		if writer != nil {
			c.audit = writer
		}
	}
}

// WithJournalPath configures the durable offline recovery journal sidecar
// file. It must be set on any Coordinator used from an HTTP-exposed process
// (so BeginRestore can persist a copy of the request outside the database)
// and on the recovery CLI (so RehydrateFromJournal can restore that request
// into a database file that a backup replacement just wiped clean).
func WithJournalPath(path string) Option {
	return func(c *Coordinator) {
		c.journalPath = strings.TrimSpace(path)
	}
}
