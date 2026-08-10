package recovery

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const journalVersion = 2

// Journal is the durable, offline record of a restore request. It lives
// outside the SQLite file specifically so it survives the moment the offline
// recovery worker replaces that file with an older backup snapshot: the
// replacement necessarily wipes out any restore_requests/recovery_control
// rows written after the backup was taken, including the very row BeginRestore
// just committed. RehydrateFromJournal uses this file to restore that state
// into the freshly replaced database before ApplyRestoreEffects runs.
type Journal struct {
	Version             int       `json:"version"`
	RequestID           string    `json:"requestId"`
	BackupID            string    `json:"backupId"`
	ActorAccountID      string    `json:"actorAccountId,omitempty"`
	State               State     `json:"state"`
	StagingPath         string    `json:"stagingPath,omitempty"`
	SafeSnapshotPath    string    `json:"safeSnapshotPath,omitempty"`
	SourceSchemaVersion *int64    `json:"sourceSchemaVersion,omitempty"`
	ReasonCode          string    `json:"reasonCode,omitempty"`
	RequestedAt         time.Time `json:"requestedAt"`

	BackupStatus        string    `json:"backupStatus"`
	BackupPath          string    `json:"backupPath"`
	BackupCanonicalPath string    `json:"backupCanonicalPath"`
	BackupSHA256        string    `json:"backupSha256"`
	BackupSizeBytes     int64     `json:"backupSizeBytes"`
	BackupSchemaVersion int64     `json:"backupSchemaVersion"`
	BackupCreatedBy     string    `json:"backupCreatedBy,omitempty"`
	BackupCreatedAt     time.Time `json:"backupCreatedAt"`
	BackupCompletedAt   time.Time `json:"backupCompletedAt"`
	BackupNotes         string    `json:"backupNotes,omitempty"`
}

type backupCatalogEntry struct {
	ID            string
	Status        string
	Path          string
	CreatedBy     string
	CreatedAt     time.Time
	CompletedAt   time.Time
	Notes         string
	SHA256        string
	SizeBytes     int64
	CanonicalPath string
	SchemaVersion int64
}

// DefaultJournalPath derives the sidecar journal path from the live SQLite
// database path. It is a plain file next to the database, never inside it,
// so it is readable even when the database file itself has just been
// replaced wholesale by an offline recovery worker.
func DefaultJournalPath(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return ""
	}
	return dbPath + ".recovery-journal.json"
}

// WriteJournal persists journal to path with 0600 permissions using a
// temp-file-then-rename sequence so a crash mid-write never leaves a
// truncated or partially written journal behind.
func WriteJournal(path string, journal Journal) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("recovery: journal path is required")
	}
	journal.Version = journalVersion
	if err := validateJournal(journal); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o600); err != nil {
		return fmt.Errorf("write journal temp file: %w", err)
	}
	if err := syncFile(tempPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("sync journal temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("install journal file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync journal directory: %w", err)
	}
	return nil
}

// ReadJournal loads a previously written journal. Callers should treat
// os.IsNotExist(err) as "no in-flight restore request", not as a fatal error.
func ReadJournal(path string) (Journal, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Journal{}, errors.New("recovery: journal path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return Journal{}, fmt.Errorf("decode journal: %w", err)
	}
	if err := validateJournal(journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func validateJournal(journal Journal) error {
	if journal.Version != journalVersion {
		return fmt.Errorf("recovery: unsupported journal version %d", journal.Version)
	}
	if strings.TrimSpace(journal.RequestID) == "" {
		return errors.New("recovery: journal is missing a request id")
	}
	if strings.TrimSpace(journal.BackupID) == "" {
		return errors.New("recovery: journal is missing a backup id")
	}
	if journal.State != StatePreparing {
		return fmt.Errorf("recovery: journal state %q cannot authorize restore", journal.State)
	}
	if journal.RequestedAt.IsZero() {
		return errors.New("recovery: journal is missing requested_at")
	}
	if strings.TrimSpace(journal.BackupStatus) != "completed" {
		return errors.New("recovery: journal backup is not completed")
	}
	if strings.TrimSpace(journal.BackupPath) == "" {
		return errors.New("recovery: journal is missing backup path")
	}
	canonical := strings.TrimSpace(journal.BackupCanonicalPath)
	if canonical == "" || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical {
		return errors.New("recovery: journal backup canonical path is invalid")
	}
	if !validSHA256Hex(journal.BackupSHA256) {
		return errors.New("recovery: journal backup sha256 is invalid")
	}
	if journal.BackupSizeBytes <= 0 {
		return errors.New("recovery: journal backup size is invalid")
	}
	if journal.BackupSchemaVersion <= 0 {
		return errors.New("recovery: journal backup schema version is invalid")
	}
	if journal.BackupCreatedAt.IsZero() {
		return errors.New("recovery: journal is missing backup created_at")
	}
	if journal.BackupCompletedAt.IsZero() {
		return errors.New("recovery: journal is missing backup completed_at")
	}
	return nil
}

func validSHA256Hex(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256HexLength || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

const sha256HexLength = 64

// RehydrateFromJournal reads the coordinator's configured journal file and,
// only if the database is missing the request it describes, recreates the
// restore_requests row and points recovery_control back at it in the
// 'preparing' state the journal captured. It reports whether it changed
// anything so callers can log accordingly. A missing journal file is not an
// error: it means no restore is in flight, or the coordinator was never
// configured with a journal path.
func (c *Coordinator) RehydrateFromJournal(ctx context.Context) (bool, error) {
	if c == nil || c.db == nil {
		return false, errors.New("recovery: database is nil")
	}
	if strings.TrimSpace(c.journalPath) == "" {
		return false, nil
	}
	journal, err := ReadJournal(c.journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return rehydrateFromJournal(ctx, c.db, c.now(), journal)
}

func rehydrateFromJournal(ctx context.Context, db *sql.DB, now time.Time, journal Journal) (bool, error) {
	if err := validateJournal(journal); err != nil {
		return false, err
	}
	requestID := strings.TrimSpace(journal.RequestID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if err := ensureBackupParentFromJournal(ctx, tx, journal); err != nil {
		return false, err
	}

	var requestExists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM restore_requests WHERE id = ?`, requestID).Scan(&requestExists)
	requestMissing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !requestMissing {
		return false, err
	}

	var controlRequestID sql.NullString
	var controlState string
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(request_id, ''), state FROM recovery_control WHERE id = 1
`).Scan(&controlRequestID, &controlState); err != nil {
		return false, err
	}
	controlNeedsRehydrate := controlRequestID.String != requestID

	if !requestMissing && !controlNeedsRehydrate {
		// Nothing was lost: either the database was never replaced, or a
		// previous run already rehydrated this exact request.
		return false, nil
	}

	if requestMissing {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO restore_requests(id, backup_id, state, staging_path, safe_snapshot_path, source_schema_version, reason_code, requested_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, requestID, journal.BackupID, string(journal.State), nullIfBlank(journal.StagingPath), nullIfBlank(journal.SafeSnapshotPath),
			journal.SourceSchemaVersion, nullIfBlank(journal.ReasonCode), formatTime(journal.RequestedAt)); err != nil {
			return false, fmt.Errorf("recovery: rehydrate restore_requests: %w", err)
		}
	}

	if controlNeedsRehydrate {
		if controlState != string(StateNormal) {
			// The control row already points at a different in-flight
			// request; overwriting it would silently abandon that request.
			return false, fmt.Errorf("recovery: cannot rehydrate %s: recovery_control already tracks %q in state %q", requestID, controlRequestID.String, controlState)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE recovery_control
SET state = ?, ready = 0, request_id = ?, staging_path = ?, safe_snapshot_path = ?,
    source_schema_version = ?, reason_code = ?, cleanup_pending = 0,
    requested_at = ?, completed_at = NULL, updated_at = ?
WHERE id = 1
`, string(journal.State), requestID, nullIfBlank(journal.StagingPath), nullIfBlank(journal.SafeSnapshotPath),
			journal.SourceSchemaVersion, nullIfBlank(journal.ReasonCode), formatTime(journal.RequestedAt), formatTime(now)); err != nil {
			return false, fmt.Errorf("recovery: rehydrate recovery_control: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func ensureBackupParentFromJournal(ctx context.Context, tx *sql.Tx, journal Journal) error {
	var existing backupCatalogEntry
	var size, schema sql.NullInt64
	var path, createdBy, createdAt, completedAt, sha, canonical sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT id, status, path, created_by, created_at, completed_at, notes, sha256, size_bytes, canonical_path, schema_version
FROM backups WHERE id = ?
`, journal.BackupID).Scan(&existing.ID, &existing.Status, &path, &createdBy, &createdAt, &completedAt, &existing.Notes, &sha, &size, &canonical, &schema)
	if errors.Is(err, sql.ErrNoRows) {
		_, insertErr := tx.ExecContext(ctx, `
INSERT INTO backups(id, status, path, created_by, created_at, completed_at, notes, sha256, size_bytes, canonical_path, schema_version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, journal.BackupID, "completed", journal.BackupPath, nullIfBlank(journal.BackupCreatedBy), formatTime(journal.BackupCreatedAt),
			formatTime(journal.BackupCompletedAt), journal.BackupNotes, journal.BackupSHA256, journal.BackupSizeBytes,
			journal.BackupCanonicalPath, journal.BackupSchemaVersion)
		if insertErr != nil {
			return fmt.Errorf("recovery: rehydrate backup parent: %w", insertErr)
		}
		return nil
	}
	if err != nil {
		return err
	}
	existing.Path = path.String
	existing.CreatedBy = createdBy.String
	existing.CreatedAt = parseTime(createdAt.String)
	existing.CompletedAt = parseTime(completedAt.String)
	existing.SHA256 = sha.String
	existing.SizeBytes = size.Int64
	existing.CanonicalPath = canonical.String
	existing.SchemaVersion = schema.Int64
	if !backupMatchesJournal(existing, journal) {
		return fmt.Errorf("recovery: existing backup parent %q conflicts with journal binding", journal.BackupID)
	}
	return nil
}

func backupMatchesJournal(existing backupCatalogEntry, journal Journal) bool {
	return existing.Status == "completed" &&
		strings.TrimSpace(existing.SHA256) == journal.BackupSHA256 &&
		existing.SizeBytes == journal.BackupSizeBytes &&
		strings.TrimSpace(existing.CanonicalPath) == journal.BackupCanonicalPath &&
		existing.SchemaVersion == journal.BackupSchemaVersion
}
