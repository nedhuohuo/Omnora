package recovery

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/audit"
	"omnora/internal/domain"
)

type Coordinator struct {
	db          *sql.DB
	clock       func() time.Time
	audit       AuditWriter
	journalPath string
}

func NewCoordinator(db *sql.DB, options ...Option) *Coordinator {
	c := &Coordinator{
		db:    db,
		clock: func() time.Time { return time.Now().UTC() },
	}
	c.audit = func(ctx context.Context, tx *sql.Tx, event AuditEvent) error {
		return audit.NewRecorder(db).RecordTx(ctx, tx, audit.Event{
			ActorAccountID: event.ActorAccountID,
			RouteGroup:     domain.RouteGroupREST,
			Action:         event.Action,
			TargetType:     event.TargetType,
			TargetID:       event.TargetID,
			MetadataJSON:   event.MetadataJSON,
		})
	}
	for _, option := range options {
		if option != nil {
			option(c)
		}
	}
	return c
}

func (c *Coordinator) Control(ctx context.Context) (Control, error) {
	if c == nil || c.db == nil {
		return Control{}, errors.New("recovery: database is nil")
	}
	return scanControl(ctx, c.db)
}

func (c *Coordinator) Request(ctx context.Context, requestID string) (RestoreRequest, error) {
	if c == nil || c.db == nil {
		return RestoreRequest{}, errors.New("recovery: database is nil")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return RestoreRequest{}, fmt.Errorf("%w: request id is required", ErrInvalidRequest)
	}
	return scanRequest(ctx, c.db, requestID)
}

// BeginRestore records an operator-confirmed restore request. It does not
// replace the live database; replacement belongs to the offline recovery
// process after staging/integrity/schema validation.
func (c *Coordinator) BeginRestore(ctx context.Context, request BeginRestoreRequest) (RestoreRequest, error) {
	if c == nil || c.db == nil {
		return RestoreRequest{}, errors.New("recovery: database is nil")
	}
	if strings.TrimSpace(c.journalPath) == "" {
		return RestoreRequest{}, fmt.Errorf("%w: recovery journal path is required", ErrInvalidRequest)
	}
	request.BackupID = strings.TrimSpace(request.BackupID)
	if request.BackupID == "" {
		return RestoreRequest{}, fmt.Errorf("%w: backup id is required", ErrInvalidRequest)
	}
	if request.RequestID == "" {
		var err error
		request.RequestID, err = newRequestID()
		if err != nil {
			return RestoreRequest{}, err
		}
	}
	now := c.now()
	backup, err := loadBackupCatalogEntry(ctx, c.db, request.BackupID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if err := bindBackupArtifact(ctx, &backup); err != nil {
		return RestoreRequest{}, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreRequest{}, err
	}
	defer tx.Rollback()
	var current State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM recovery_control WHERE id = 1`).Scan(&current); err != nil {
		return RestoreRequest{}, err
	}
	if current != StateNormal {
		return RestoreRequest{}, ErrRestoreInProgress
	}
	rechecked, err := loadBackupCatalogEntry(ctx, tx, request.BackupID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if !backupCatalogEntriesMatch(backup, rechecked) {
		return RestoreRequest{}, fmt.Errorf("%w: backup catalog changed during approval", ErrInvalidRequest)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO restore_requests(id, backup_id, state, staging_path, requested_at)
VALUES (?, ?, 'preparing', ?, ?)
`, request.RequestID, request.BackupID, nullIfBlank(request.StagingPath), formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recovery_control
SET state = 'preparing', ready = 0, request_id = ?, staging_path = ?,
    reason_code = NULL, cleanup_pending = 0, requested_at = ?, completed_at = NULL,
    updated_at = ?
WHERE id = 1 AND state = 'normal'
`, request.RequestID, nullIfBlank(request.StagingPath), formatTime(now), formatTime(now))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRestoreInProgress
	}
	if err := c.writeTransitionAudit(ctx, tx, request.ActorAccountID, request.RequestID, StateNormal, StatePreparing, map[string]any{
		"backupId": request.BackupID,
	}); err != nil {
		return RestoreRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RestoreRequest{}, err
	}
	created := RestoreRequest{
		ID: request.RequestID, BackupID: request.BackupID, State: StatePreparing,
		StagingPath: request.StagingPath, RequestedAt: now,
	}
	journal := Journal{
		RequestID: request.RequestID, BackupID: request.BackupID, ActorAccountID: request.ActorAccountID,
		State: StatePreparing, StagingPath: request.StagingPath, RequestedAt: now,
		BackupStatus: backup.Status, BackupPath: backup.Path, BackupCanonicalPath: backup.CanonicalPath,
		BackupSHA256: backup.SHA256, BackupSizeBytes: backup.SizeBytes, BackupSchemaVersion: backup.SchemaVersion,
		BackupCreatedBy: backup.CreatedBy, BackupCreatedAt: backup.CreatedAt, BackupCompletedAt: backup.CompletedAt,
		BackupNotes: backup.Notes,
	}
	if err := WriteJournal(c.journalPath, journal); err != nil {
		markErrPrefix := ""
		if _, markErr := c.MarkRecoveryRequired(ctx, request.RequestID, request.ActorAccountID, "journal_unavailable"); markErr != nil {
			markErrPrefix = fmt.Sprintf("; additionally failed to mark recovery_required: %v", markErr)
		}
		return created, fmt.Errorf("recovery: restore request committed but journal write failed%s: %w", markErrPrefix, err)
	}
	return created, nil
}

func loadBackupCatalogEntry(ctx context.Context, source rowQuerier, backupID string) (backupCatalogEntry, error) {
	backupID = strings.TrimSpace(backupID)
	if backupID == "" {
		return backupCatalogEntry{}, fmt.Errorf("%w: backup id is required", ErrInvalidRequest)
	}
	var entry backupCatalogEntry
	var path, createdBy, createdAt, completedAt, sha, canonical sql.NullString
	var size, schema sql.NullInt64
	err := source.QueryRowContext(ctx, `
SELECT id, status, path, created_by, created_at, completed_at, notes, sha256, size_bytes, canonical_path, schema_version
FROM backups WHERE id = ?
`, backupID).Scan(&entry.ID, &entry.Status, &path, &createdBy, &createdAt, &completedAt, &entry.Notes, &sha, &size, &canonical, &schema)
	if errors.Is(err, sql.ErrNoRows) {
		return backupCatalogEntry{}, fmt.Errorf("%w: backup was not found", ErrInvalidRequest)
	}
	if err != nil {
		return backupCatalogEntry{}, err
	}
	entry.Path = strings.TrimSpace(path.String)
	entry.CreatedBy = strings.TrimSpace(createdBy.String)
	entry.CreatedAt = parseTime(createdAt.String)
	entry.CompletedAt = parseTime(completedAt.String)
	entry.SHA256 = strings.TrimSpace(sha.String)
	entry.SizeBytes = size.Int64
	entry.CanonicalPath = strings.TrimSpace(canonical.String)
	entry.SchemaVersion = schema.Int64
	return entry, nil
}

func bindBackupArtifact(ctx context.Context, entry *backupCatalogEntry) error {
	if entry == nil {
		return fmt.Errorf("%w: backup is required", ErrInvalidRequest)
	}
	if entry.Status != "completed" {
		return fmt.Errorf("%w: backup is not completed", ErrInvalidRequest)
	}
	if strings.TrimSpace(entry.Path) == "" {
		return fmt.Errorf("%w: backup path is missing", ErrInvalidRequest)
	}
	if !validSHA256Hex(entry.SHA256) || entry.SizeBytes <= 0 || entry.SchemaVersion <= 0 || entry.CanonicalPath == "" {
		return fmt.Errorf("%w: backup provenance is incomplete", ErrInvalidRequest)
	}
	if entry.CreatedAt.IsZero() || entry.CompletedAt.IsZero() {
		return fmt.Errorf("%w: backup timestamps are incomplete", ErrInvalidRequest)
	}
	canonical, err := canonicalRegularFilePath(entry.Path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if canonical != entry.CanonicalPath {
		return fmt.Errorf("%w: backup canonical path does not match provenance", ErrInvalidRequest)
	}
	hash, size, err := hashRegularFile(canonical)
	if err != nil {
		return fmt.Errorf("%w: hash backup artifact: %v", ErrInvalidRequest, err)
	}
	if hash != entry.SHA256 || size != entry.SizeBytes {
		return fmt.Errorf("%w: backup artifact hash/size does not match provenance", ErrInvalidRequest)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

func canonicalRegularFilePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("backup path is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("backup must be a real regular file")
	}
	return absolute, nil
}

func backupCatalogEntriesMatch(left, right backupCatalogEntry) bool {
	return left.ID == right.ID && left.Status == right.Status && left.Path == right.Path &&
		left.CreatedBy == right.CreatedBy && left.CreatedAt.Equal(right.CreatedAt) && left.CompletedAt.Equal(right.CompletedAt) &&
		left.Notes == right.Notes && left.SHA256 == right.SHA256 && left.SizeBytes == right.SizeBytes &&
		left.CanonicalPath == right.CanonicalPath && left.SchemaVersion == right.SchemaVersion
}

func (c *Coordinator) MarkRestoring(ctx context.Context, requestID, actorAccountID string) (RestoreRequest, error) {
	return c.transitionRequest(ctx, requestID, actorAccountID, StateRestoring, "")
}

// RecordArtifacts persists staging/safe-snapshot metadata after the offline
// coordinator has created or validated those files. Paths are never copied to
// audit metadata; only presence is recorded there.
func (c *Coordinator) RecordArtifacts(ctx context.Context, requestID, actorAccountID string, update ArtifactUpdate) (RestoreRequest, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return RestoreRequest{}, fmt.Errorf("%w: request id is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(update.StagingPath) == "" && strings.TrimSpace(update.SafeSnapshotPath) == "" && update.SourceSchemaVersion == nil {
		return RestoreRequest{}, fmt.Errorf("%w: at least one artifact field is required", ErrInvalidRequest)
	}
	now := c.now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreRequest{}, err
	}
	defer tx.Rollback()
	request, err := scanRequest(ctx, tx, requestID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if request.State != StatePreparing && request.State != StateRestoring && request.State != StateFinalizeRequired {
		return RestoreRequest{}, fmt.Errorf("%w: artifacts cannot be recorded from %s", ErrInvalidTransition, request.State)
	}
	var current State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM recovery_control WHERE id = 1 AND request_id = ?`, requestID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RestoreRequest{}, ErrRequestMismatch
		}
		return RestoreRequest{}, err
	}
	if current != request.State {
		return RestoreRequest{}, fmt.Errorf("%w: request=%s control=%s", ErrRequestMismatch, request.State, current)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE restore_requests
SET staging_path = COALESCE(?, staging_path),
    source_schema_version = COALESCE(?, source_schema_version),
    safe_snapshot_path = COALESCE(?, safe_snapshot_path)
WHERE id = ? AND state = ?
`, nullIfBlank(update.StagingPath), update.SourceSchemaVersion, nullIfBlank(update.SafeSnapshotPath), requestID, string(request.State))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	result, err = tx.ExecContext(ctx, `
UPDATE recovery_control
SET staging_path = COALESCE(?, staging_path),
    source_schema_version = COALESCE(?, source_schema_version),
    safe_snapshot_path = COALESCE(?, safe_snapshot_path), updated_at = ?
WHERE id = 1 AND request_id = ? AND state = ?
`, nullIfBlank(update.StagingPath), update.SourceSchemaVersion, nullIfBlank(update.SafeSnapshotPath), formatTime(now), requestID, string(request.State))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	fields := map[string]any{
		"stagingRecorded":       strings.TrimSpace(update.StagingPath) != "",
		"schemaVersionRecorded": update.SourceSchemaVersion != nil,
		"safeSnapshotRecorded":  strings.TrimSpace(update.SafeSnapshotPath) != "",
	}
	if err := c.writeAudit(ctx, tx, actorAccountID, requestID, "recovery_artifacts_recorded", fields); err != nil {
		return RestoreRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RestoreRequest{}, err
	}
	if strings.TrimSpace(update.StagingPath) != "" {
		request.StagingPath = update.StagingPath
	}
	if update.SourceSchemaVersion != nil {
		request.SourceSchemaVersion = sql.NullInt64{Int64: *update.SourceSchemaVersion, Valid: true}
	}
	if strings.TrimSpace(update.SafeSnapshotPath) != "" {
		request.SafeSnapshotPath = update.SafeSnapshotPath
	}
	return request, nil
}

func (c *Coordinator) MarkRecoveryRequired(ctx context.Context, requestID, actorAccountID, reasonCode string) (RestoreRequest, error) {
	requestID = strings.TrimSpace(requestID)
	reasonCode = strings.TrimSpace(reasonCode)
	if requestID == "" || reasonCode == "" {
		return RestoreRequest{}, fmt.Errorf("%w: request and reason are required", ErrInvalidRequest)
	}
	return c.transitionRequest(ctx, requestID, actorAccountID, StateRecoveryRequired, reasonCode)
}

// ApplyRestoreEffects is called only after a validated snapshot has replaced
// the live database in the offline recovery process. It atomically invalidates
// every credential class and puts the control plane behind bootstrap gates.
func (c *Coordinator) ApplyRestoreEffects(ctx context.Context, requestID, actorAccountID string) (RestoreRequest, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return RestoreRequest{}, fmt.Errorf("%w: request id is required", ErrInvalidRequest)
	}
	now := c.now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreRequest{}, err
	}
	defer tx.Rollback()
	request, err := scanRequest(ctx, tx, requestID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if request.State != StateRestoring {
		return RestoreRequest{}, fmt.Errorf("%w: want restoring, got %s", ErrInvalidTransition, request.State)
	}
	var current State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM recovery_control WHERE id = 1 AND request_id = ?`, requestID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RestoreRequest{}, ErrRequestMismatch
		}
		return RestoreRequest{}, err
	}
	if current != StateRestoring {
		return RestoreRequest{}, fmt.Errorf("%w: control state is %s", ErrInvalidTransition, current)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE system_state
SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT), updated_at = ?
WHERE key = 'credential_generation'
`, formatTime(now))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, errors.New("recovery: credential generation is missing")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE accounts
SET totp_required = 0,
    totp_secret_ciphertext = NULL,
    totp_pending_secret_ciphertext = NULL,
    totp_pending_expires_at = NULL,
    totp_confirmed_at = NULL,
    password_reset_required = 1,
    totp_reset_required = 0,
    updated_at = ?
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE identity_sessions SET revoked_at = ? WHERE revoked_at IS NULL
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE browser_sessions SET revoked_at = ? WHERE revoked_at IS NULL
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE ai_tokens SET revoked_at = ?, updated_at = ? WHERE revoked_at IS NULL
`, formatTime(now), formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE shares
SET revoked_at = ?, invalidated_at = ?, invalidated_reason = 'restore',
    credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER),
    generation = generation + 1, updated_at = ?
WHERE revoked_at IS NULL
`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE share_sessions SET revoked_at = ? WHERE revoked_at IS NULL
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE share_download_tickets
SET status = 'canceled', canceled_at = ?
WHERE status IN ('issued', 'streaming')
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE upload_sessions
SET status = 'canceled', canceled_at = ?, cleanup_pending = 1,
    credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE status = 'active'
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	storageKindColumn, err := recoveryMountStorageKindColumn(ctx, tx)
	if err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mounts SET status = 'disabled', updated_at = ?
WHERE `+storageKindColumn+` = 'external' AND status <> 'deleted'
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET status = 'paused', updated_at = ?
WHERE status = 'running'
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE file_operations SET status = 'recovery_required', last_error = 'restore in progress', updated_at = ?
WHERE status NOT IN ('completed', 'recovery_required')
`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_confirmations SET status = 'expired' WHERE status = 'pending'`); err != nil {
		return RestoreRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_transfer_tickets SET status = 'canceled', closed_at = ? WHERE status = 'active'`, formatTime(now)); err != nil {
		return RestoreRequest{}, err
	}
	result, err = tx.ExecContext(ctx, `
UPDATE restore_requests
SET state = 'finalize_required', reason_code = 'credentials_revoked', completed_at = NULL
WHERE id = ? AND state = 'restoring'
`, requestID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	result, err = tx.ExecContext(ctx, `
UPDATE recovery_control
SET state = 'finalize_required', ready = 0, reason_code = 'credentials_revoked', completed_at = NULL,
    updated_at = ?
WHERE id = 1 AND request_id = ? AND state = 'restoring'
`, formatTime(now), requestID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	if err := c.writeTransitionAudit(ctx, tx, actorAccountID, requestID, StateRestoring, StateFinalizeRequired, map[string]any{
		"credentialGenerationRotated": true,
		"credentialsRevoked":          true,
	}); err != nil {
		return RestoreRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RestoreRequest{}, err
	}
	request.State = StateFinalizeRequired
	request.ReasonCode = "credentials_revoked"
	return request, nil
}

func recoveryMountStorageKindColumn(ctx context.Context, tx *sql.Tx) (string, error) {
	for _, column := range []string{"storage_kind", "kind"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('mounts') WHERE name = ?`, column).Scan(&count); err != nil {
			return "", err
		}
		if count == 1 {
			return column, nil
		}
	}
	return "", errors.New("recovery: mount storage-kind column is missing")
}

// MarkNormalPendingBootstrap completes offline cleanup without declaring the
// service ready. A new process must perform bootstrap and call CompleteBootstrap.
func (c *Coordinator) MarkNormalPendingBootstrap(ctx context.Context, requestID, actorAccountID string) (RestoreRequest, error) {
	return c.transitionRequest(ctx, requestID, actorAccountID, StateNormalPendingBootstrap, "")
}

// MarkCleanupPending records that staging or the pre-restore safe snapshot
// still needs operator cleanup. It never makes the process ready.
func (c *Coordinator) MarkCleanupPending(ctx context.Context, requestID, actorAccountID, reasonCode string) (RestoreRequest, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return RestoreRequest{}, fmt.Errorf("%w: request id is required", ErrInvalidRequest)
	}
	reasonCode = strings.TrimSpace(reasonCode)
	if reasonCode == "" {
		reasonCode = "cleanup_pending"
	}
	now := c.now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreRequest{}, err
	}
	defer tx.Rollback()
	request, err := scanRequest(ctx, tx, requestID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if request.State != StateFinalizeRequired && request.State != StateNormalPendingBootstrap {
		return RestoreRequest{}, fmt.Errorf("%w: cleanup cannot be recorded from %s", ErrInvalidTransition, request.State)
	}
	var current State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM recovery_control WHERE id = 1 AND request_id = ?`, requestID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RestoreRequest{}, ErrRequestMismatch
		}
		return RestoreRequest{}, err
	}
	if current != request.State {
		return RestoreRequest{}, fmt.Errorf("%w: request=%s control=%s", ErrRequestMismatch, request.State, current)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE restore_requests
SET cleanup_pending = 1, reason_code = ?
WHERE id = ? AND state = ?
`, reasonCode, requestID, string(request.State))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	result, err = tx.ExecContext(ctx, `
UPDATE recovery_control
SET cleanup_pending = 1, reason_code = ?, ready = 0, updated_at = ?
WHERE id = 1 AND request_id = ? AND state = ?
`, reasonCode, formatTime(now), requestID, string(request.State))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	if err := c.writeTransitionAudit(ctx, tx, actorAccountID, requestID, request.State, request.State, map[string]any{
		"reasonCode":     reasonCode,
		"cleanupPending": true,
	}); err != nil {
		return RestoreRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RestoreRequest{}, err
	}
	request.CleanupPending = true
	request.ReasonCode = reasonCode
	return request, nil
}

// CompleteBootstrap is intentionally the only transition that returns the
// control plane to normal. It leaves ready=false; the server's aggregate
// readiness gate must pass before MarkReady is called.
func (c *Coordinator) CompleteBootstrap(ctx context.Context, requestID, actorAccountID string) (RestoreRequest, error) {
	return c.transitionRequest(ctx, requestID, actorAccountID, StateNormal, "")
}

func (c *Coordinator) transitionRequest(ctx context.Context, requestID, actorAccountID string, target State, reason string) (RestoreRequest, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return RestoreRequest{}, fmt.Errorf("%w: request id is required", ErrInvalidRequest)
	}
	now := c.now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreRequest{}, err
	}
	defer tx.Rollback()
	request, err := scanRequest(ctx, tx, requestID)
	if err != nil {
		return RestoreRequest{}, err
	}
	if !allowedTransition(request.State, target) {
		return RestoreRequest{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, request.State, target)
	}
	var current State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM recovery_control WHERE id = 1 AND request_id = ?`, requestID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RestoreRequest{}, ErrRequestMismatch
		}
		return RestoreRequest{}, err
	}
	if current != request.State {
		return RestoreRequest{}, fmt.Errorf("%w: request=%s control=%s", ErrRequestMismatch, request.State, current)
	}
	if target == StateNormal && request.State != StateNormalPendingBootstrap {
		return RestoreRequest{}, ErrNotReadyForBootstrap
	}
	if reason == "" {
		reason = request.ReasonCode
	}
	// restore_requests.state has a narrower CHECK than recovery_control.state:
	// it records terminal outcomes as 'completed'/'failed' rather than the
	// live control-plane state name 'normal'. recovery_control keeps 'normal'
	// so /readyz and Control() report the same vocabulary as every other
	// state.
	requestTargetState := string(target)
	if target == StateNormal {
		requestTargetState = "completed"
	}
	result, err := tx.ExecContext(ctx, `
UPDATE restore_requests
SET state = ?, reason_code = ?, completed_at = CASE WHEN ? = 'normal' THEN ? ELSE completed_at END
WHERE id = ? AND state = ?
`, requestTargetState, nullIfBlank(reason), string(target), formatTime(now), requestID, string(request.State))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	result, err = tx.ExecContext(ctx, `
UPDATE recovery_control
SET state = ?, ready = 0, reason_code = ?, completed_at = CASE WHEN ? = 'normal' THEN ? ELSE completed_at END,
    updated_at = ?
WHERE id = 1 AND request_id = ? AND state = ?
`, string(target), nullIfBlank(reason), string(target), formatTime(now), formatTime(now), requestID, string(request.State))
	if err != nil {
		return RestoreRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RestoreRequest{}, err
	} else if affected != 1 {
		return RestoreRequest{}, ErrRequestMismatch
	}
	if err := c.writeTransitionAudit(ctx, tx, actorAccountID, requestID, request.State, target, map[string]any{
		"reasonCode": reason,
	}); err != nil {
		return RestoreRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RestoreRequest{}, err
	}
	request.State = target
	request.ReasonCode = reason
	if target == StateNormal {
		request.CompletedAt = now
	}
	return request, nil
}

func allowedTransition(from, to State) bool {
	switch from {
	case StatePreparing:
		return to == StateRestoring || to == StateRecoveryRequired
	case StateRecoveryRequired:
		return to == StateRestoring || to == StateRecoveryRequired
	case StateRestoring:
		return to == StateFinalizeRequired || to == StateRecoveryRequired
	case StateFinalizeRequired:
		return to == StateNormalPendingBootstrap || to == StateRecoveryRequired
	case StateNormalPendingBootstrap:
		return to == StateNormal || to == StateRecoveryRequired
	default:
		return false
	}
}

func (c *Coordinator) writeTransitionAudit(ctx context.Context, tx *sql.Tx, actor, requestID string, from, to State, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["from"] = string(from)
	fields["to"] = string(to)
	return c.writeAudit(ctx, tx, actor, requestID, "recovery_state_transition", fields)
}

func (c *Coordinator) writeAudit(ctx context.Context, tx *sql.Tx, actor, requestID, action string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return c.audit(ctx, tx, AuditEvent{
		Action:         action,
		TargetType:     "restore_request",
		TargetID:       requestID,
		ActorAccountID: strings.TrimSpace(actor),
		MetadataJSON:   string(metadata),
	})
}

func scanControl(ctx context.Context, source interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Control, error) {
	var state string
	var ready, cleanup int
	var requestID, staging, safeSnapshot, reason string
	var sourceVersion sql.NullInt64
	var requestedAt, completedAt, updatedAt sql.NullString
	err := source.QueryRowContext(ctx, `
SELECT state, ready, COALESCE(request_id, ''), COALESCE(staging_path, ''), source_schema_version,
       COALESCE(safe_snapshot_path, ''), COALESCE(reason_code, ''), cleanup_pending,
       COALESCE(requested_at, ''), COALESCE(completed_at, ''), updated_at
FROM recovery_control WHERE id = 1
`).Scan(&state, &ready, &requestID, &staging, &sourceVersion, &safeSnapshot, &reason, &cleanup, &requestedAt, &completedAt, &updatedAt)
	if err != nil {
		return Control{}, err
	}
	return Control{
		State: State(state), Ready: ready == 1, RequestID: requestID, StagingPath: staging,
		SourceSchemaVersion: sourceVersion, SafeSnapshotPath: safeSnapshot, ReasonCode: reason,
		CleanupPending: cleanup == 1, RequestedAt: parseTime(requestedAt.String),
		CompletedAt: parseTime(completedAt.String), UpdatedAt: parseTime(updatedAt.String),
	}, nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanRequest(ctx context.Context, source rowQuerier, requestID string) (RestoreRequest, error) {
	var request RestoreRequest
	var state, staging, safeSnapshot, reason string
	var sourceVersion sql.NullInt64
	var cleanup int
	var requestedAt, completedAt sql.NullString
	err := source.QueryRowContext(ctx, `
SELECT id, backup_id, state, COALESCE(staging_path, ''), source_schema_version,
       COALESCE(safe_snapshot_path, ''), COALESCE(reason_code, ''), cleanup_pending,
       requested_at, COALESCE(completed_at, '')
FROM restore_requests WHERE id = ?
`, requestID).Scan(&request.ID, &request.BackupID, &state, &staging, &sourceVersion, &safeSnapshot, &reason, &cleanup, &requestedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RestoreRequest{}, ErrRequestNotFound
	}
	if err != nil {
		return RestoreRequest{}, err
	}
	request.State = State(state)
	request.StagingPath = staging
	request.SourceSchemaVersion = sourceVersion
	request.SafeSnapshotPath = safeSnapshot
	request.ReasonCode = reason
	request.CleanupPending = cleanup == 1
	request.RequestedAt = parseTime(requestedAt.String)
	request.CompletedAt = parseTime(completedAt.String)
	return request, nil
}

func (c *Coordinator) now() time.Time {
	if c == nil || c.clock == nil {
		return time.Now().UTC()
	}
	return c.clock().UTC().Round(0)
}

func newRequestID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "restore_" + hex.EncodeToString(bytes[:]), nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
