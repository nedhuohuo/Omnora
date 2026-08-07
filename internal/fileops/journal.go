// Package fileops provides a durable operation journal for file mutations
// (rename, move, delete, trash, trash restore). Every mutation is prepared
// as a database transaction that records the operation intent, invalidates
// affected shares, and writes its audit event together; filesystem I/O only
// begins after that transaction commits, and the journal row is then
// advanced to completed or recovery_required based on the outcome. This
// closes the gap where authorization state and audit trail could silently
// diverge from what actually happened on disk.
package fileops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Kind mirrors the file_operations.kind CHECK constraint in migration 008.
type Kind string

const (
	KindRename       Kind = "same_mount_rename"
	KindMove         Kind = "same_mount_move"
	KindDelete       Kind = "delete"
	KindTrash        Kind = "trash"
	KindTrashRestore Kind = "trash_restore"
)

// Status mirrors the file_operations.status CHECK constraint in migration 008.
type Status string

const (
	StatusPrepared         Status = "prepared"
	StatusCompleted        Status = "completed"
	StatusRecoveryRequired Status = "recovery_required"
)

var (
	// ErrOperationNotFound is returned when a status transition does not
	// match exactly one row in the expected prior state.
	ErrOperationNotFound = errors.New("fileops: operation not found or already advanced")
	// ErrJournalNotConfigured guards against a nil-database journal being
	// used to authorize a mutation.
	ErrJournalNotConfigured = errors.New("fileops: journal is not configured")
)

// OperationSpec describes one durable file mutation intent.
type OperationSpec struct {
	ID              string
	Kind            Kind
	SpaceID         string
	MountID         string
	SourcePath      string
	DestinationPath string
	ActorAccountID  string
}

// Operation is the durable, read-back view of a journaled mutation.
type Operation struct {
	ID              string
	Kind            Kind
	Status          Status
	SourcePath      string
	DestinationPath string
	LastError       string
}

// TxFunc runs inside the same transaction as the operation insert. Callers
// use it for share invalidation and audit so a mutation can never become
// durable without its authorization side effects and audit trail.
type TxFunc func(ctx context.Context, tx *sql.Tx) error

// Journal persists file_operations rows as the single source of truth for
// an in-flight filesystem mutation.
type Journal struct {
	db  *sql.DB
	now func() time.Time
}

// NewJournal constructs a Journal backed by db.
func NewJournal(db *sql.DB) *Journal {
	return &Journal{db: db, now: time.Now}
}

// Prepare inserts the durable operation row and then runs sideEffects (share
// invalidation, audit RecordTx, ...) in the same transaction. Callers must
// not begin filesystem I/O until Prepare returns a nil error, and must call
// Complete or RequireRecovery afterward so the row never remains "prepared"
// forever without also reflecting a filesystem outcome.
func (j *Journal) Prepare(ctx context.Context, spec OperationSpec, sideEffects ...TxFunc) error {
	if j == nil || j.db == nil {
		return ErrJournalNotConfigured
	}
	if spec.ID == "" || spec.Kind == "" {
		return errors.New("fileops: operation id and kind are required")
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fileops: begin prepare: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
INSERT INTO file_operations(
    id, kind, status,
    source_space_id, source_mount_id, source_relative_path,
    destination_space_id, destination_mount_id, destination_relative_path,
    actor_account_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, spec.ID, string(spec.Kind), string(StatusPrepared),
		nullableString(spec.SpaceID), nullableString(spec.MountID), nullableString(spec.SourcePath),
		nullableString(spec.SpaceID), nullableString(spec.MountID), nullableString(spec.DestinationPath),
		nullableString(spec.ActorAccountID))
	if err != nil {
		return fmt.Errorf("fileops: insert operation: %w", err)
	}

	for _, effect := range sideEffects {
		if effect == nil {
			continue
		}
		if err := effect(ctx, tx); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fileops: commit prepare: %w", err)
	}
	return nil
}

// Complete advances a prepared operation to completed after its filesystem
// I/O has finished successfully.
func (j *Journal) Complete(ctx context.Context, operationID string) error {
	if j == nil || j.db == nil {
		return ErrJournalNotConfigured
	}
	now := formatTime(j.now())
	result, err := j.db.ExecContext(ctx, `
UPDATE file_operations
SET status = ?, completed_at = ?, updated_at = ?
WHERE id = ? AND status = ?
`, string(StatusCompleted), now, now, operationID, string(StatusPrepared))
	if err != nil {
		return fmt.Errorf("fileops: complete operation: %w", err)
	}
	return requireOneRowAffected(result)
}

// RequireRecovery marks a prepared operation as needing manual or automated
// recovery after its filesystem I/O failed or returned an ambiguous result.
// The durable row and last error remain so a later recovery pass can
// reconcile database and filesystem state; nothing is deleted.
func (j *Journal) RequireRecovery(ctx context.Context, operationID string, cause error) error {
	if j == nil || j.db == nil {
		return ErrJournalNotConfigured
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	now := formatTime(j.now())
	result, err := j.db.ExecContext(ctx, `
UPDATE file_operations
SET status = ?, last_error = ?, updated_at = ?
WHERE id = ? AND status = ?
`, string(StatusRecoveryRequired), message, now, operationID, string(StatusPrepared))
	if err != nil {
		return fmt.Errorf("fileops: require recovery: %w", err)
	}
	return requireOneRowAffected(result)
}

// Get returns the durable state of one operation, primarily for tests and
// operator inspection.
func (j *Journal) Get(ctx context.Context, operationID string) (Operation, error) {
	if j == nil || j.db == nil {
		return Operation{}, ErrJournalNotConfigured
	}
	var op Operation
	var kind, status string
	var sourcePath, destPath, lastError sql.NullString
	err := j.db.QueryRowContext(ctx, `
SELECT id, kind, status, source_relative_path, destination_relative_path, COALESCE(last_error, '')
FROM file_operations
WHERE id = ?
`, operationID).Scan(&op.ID, &kind, &status, &sourcePath, &destPath, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	op.Kind = Kind(kind)
	op.Status = Status(status)
	op.SourcePath = sourcePath.String
	op.DestinationPath = destPath.String
	op.LastError = lastError.String
	return op, nil
}

func requireOneRowAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrOperationNotFound
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
