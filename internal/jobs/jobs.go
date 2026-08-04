package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

var ErrInvalidJSON = errors.New("invalid json object")

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) Store {
	return Store{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

type Job struct {
	ID             string
	Kind           string
	Priority       int
	Status         Status
	PayloadJSON    string
	CheckpointJSON string
	Attempts       int
	MaxAttempts    int
	ClaimedAt      sql.NullString
	ClaimedBy      sql.NullString
	LastError      sql.NullString
	CreatedAt      string
	UpdatedAt      string
	CompletedAt    sql.NullString
}

type EnqueueOptions struct {
	ID             string
	Kind           string
	Priority       int
	PayloadJSON    string
	CheckpointJSON string
	MaxAttempts    int
}

func (s Store) Enqueue(ctx context.Context, opts EnqueueOptions) (Job, error) {
	if err := s.requireDB(); err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(opts.Kind) == "" {
		return Job{}, errors.New("job kind is required")
	}
	payload, err := normalizeJSONObject(opts.PayloadJSON)
	if err != nil {
		return Job{}, err
	}
	checkpoint, err := normalizeJSONObject(opts.CheckpointJSON)
	if err != nil {
		return Job{}, err
	}
	if opts.ID == "" {
		var err error
		opts.ID, err = newID()
		if err != nil {
			return Job{}, err
		}
	}
	if opts.Priority == 0 {
		opts.Priority = 100
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}

	now := s.now().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO jobs(id, kind, priority, status, payload_json, checkpoint_json, attempts, max_attempts, created_at, updated_at)
VALUES (?, ?, ?, 'queued', ?, ?, 0, ?, ?, ?)
`, opts.ID, opts.Kind, opts.Priority, payload, checkpoint, opts.MaxAttempts, now, now)
	if err != nil {
		return Job{}, err
	}
	return s.Get(ctx, opts.ID)
}

func (s Store) Claim(ctx context.Context, workerID string) (*Job, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("worker id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRowContext(ctx, `
SELECT id
FROM jobs
WHERE status = 'queued'
	AND NOT EXISTS (SELECT 1 FROM jobs WHERE status = 'running')
ORDER BY priority ASC, created_at ASC, id ASC
LIMIT 1
`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	now := s.now().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = 'running', claimed_at = ?, claimed_by = ?, updated_at = ?
WHERE id = ? AND status = 'queued'
	AND NOT EXISTS (
		SELECT 1 FROM jobs WHERE status = 'running' AND id <> ?
	)
`, now, workerID, now, id, id)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, nil
	}

	job, err := getTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// ClaimByID atomically claims the requested queued job when no job is running.
// It never substitutes a different queued job for the requested ID.
func (s Store) ClaimByID(ctx context.Context, id, workerID string) (*Job, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("job id is required")
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("worker id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := s.now().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = 'running', claimed_at = ?, claimed_by = ?, updated_at = ?
WHERE id = ? AND status = 'queued'
	AND NOT EXISTS (SELECT 1 FROM jobs WHERE status = 'running')
`, now, workerID, now, id)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, nil
	}

	job, err := getTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s Store) SaveCheckpoint(ctx context.Context, id, checkpointJSON string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	checkpoint, err := normalizeJSONObject(checkpointJSON)
	if err != nil {
		return err
	}
	now := s.now().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
UPDATE jobs
SET checkpoint_json = ?, updated_at = ?
WHERE id = ? AND status = 'running'
`, checkpoint, now, id)
	return err
}

// Requeue saves a checkpoint and releases a running job for its next batch.
// Yielding after a successful batch is not a failed attempt.
func (s Store) Requeue(ctx context.Context, id, checkpointJSON string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	checkpoint, err := normalizeJSONObject(checkpointJSON)
	if err != nil {
		return err
	}
	now := s.now().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status = 'queued', checkpoint_json = ?, claimed_at = NULL, claimed_by = NULL,
	last_error = NULL, updated_at = ?
WHERE id = ? AND status = 'running'
`, checkpoint, now, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return errors.New("job is not running")
	}
	return nil
}

func (s Store) Pause(ctx context.Context, id string) error {
	return s.setStatus(ctx, id, StatusPaused, StatusQueued, StatusRunning)
}

func (s Store) Resume(ctx context.Context, id string) error {
	return s.setStatus(ctx, id, StatusQueued, StatusPaused)
}

func (s Store) Complete(ctx context.Context, id string) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	now := s.now().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status = 'completed', claimed_at = NULL, claimed_by = NULL, completed_at = ?, updated_at = ?
WHERE id = ? AND status = 'running'
`, now, now, id)
	return err
}

func (s Store) Fail(ctx context.Context, id string, cause error) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	now := s.now().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET attempts = attempts + 1,
	status = CASE WHEN attempts + 1 >= max_attempts THEN 'failed' ELSE 'queued' END,
	claimed_at = NULL,
	claimed_by = NULL,
	last_error = ?,
	updated_at = ?
WHERE id = ? AND status = 'running'
`, message, now, id)
	return err
}

func (s Store) Get(ctx context.Context, id string) (Job, error) {
	if err := s.requireDB(); err != nil {
		return Job{}, err
	}
	return getRow(s.db.QueryRowContext(ctx, `
SELECT id, kind, priority, status, payload_json, checkpoint_json, attempts, max_attempts,
	claimed_at, claimed_by, last_error, created_at, updated_at, completed_at
FROM jobs
WHERE id = ?
`, id))
}

func (s Store) setStatus(ctx context.Context, id string, next Status, allowed ...Status) error {
	if err := s.requireDB(); err != nil {
		return err
	}
	placeholders := make([]string, 0, len(allowed))
	args := []any{string(next), s.now().Format(time.RFC3339Nano), id}
	for _, status := range allowed {
		placeholders = append(placeholders, "?")
		args = append(args, string(status))
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status = ?, claimed_at = NULL, claimed_by = NULL, updated_at = ?
WHERE id = ? AND status IN (`+strings.Join(placeholders, ",")+`)
`, args...)
	return err
}

func (s Store) requireDB() error {
	if s.db == nil {
		return errors.New("jobs database is nil")
	}
	return nil
}

func getTx(ctx context.Context, tx *sql.Tx, id string) (Job, error) {
	return getRow(tx.QueryRowContext(ctx, `
SELECT id, kind, priority, status, payload_json, checkpoint_json, attempts, max_attempts,
	claimed_at, claimed_by, last_error, created_at, updated_at, completed_at
FROM jobs
WHERE id = ?
`, id))
}

type scanner interface {
	Scan(dest ...any) error
}

func getRow(row scanner) (Job, error) {
	var job Job
	var status string
	err := row.Scan(
		&job.ID,
		&job.Kind,
		&job.Priority,
		&status,
		&job.PayloadJSON,
		&job.CheckpointJSON,
		&job.Attempts,
		&job.MaxAttempts,
		&job.ClaimedAt,
		&job.ClaimedBy,
		&job.LastError,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.CompletedAt,
	)
	job.Status = Status(status)
	return job, err
}

func normalizeJSONObject(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "{}", nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return "", ErrInvalidJSON
	}
	if _, ok := decoded.(map[string]any); !ok {
		return "", ErrInvalidJSON
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(b[:]), nil
}
