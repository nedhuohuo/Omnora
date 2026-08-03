package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultSessionTTL = 15 * time.Minute

type ErrorCode string

const (
	CodeShareSecretMissing ErrorCode = "share_secret_missing"
	CodePasswordRequired   ErrorCode = "password_required"
	CodeShareUnavailable   ErrorCode = "share_unavailable"
	CodeRateLimited        ErrorCode = "rate_limited"
)

type ExchangeError struct {
	Code ErrorCode
	Err  error
}

func (e *ExchangeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return string(e.Code) + ": " + e.Err.Error()
	}
	return string(e.Code)
}

func (e *ExchangeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Service struct {
	db         *sql.DB
	now        func() time.Time
	sessionTTL time.Duration
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

func WithSessionTTL(ttl time.Duration) Option {
	return func(s *Service) {
		if ttl > 0 {
			s.sessionTTL = ttl
		}
	}
}

func NewService(db *sql.DB, opts ...Option) *Service {
	s := &Service{
		db:         db,
		now:        time.Now,
		sessionTTL: defaultSessionTTL,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type ExchangeRequest struct {
	PublicID       string
	FragmentSecret string
	Password       string
}

type ExchangeResult struct {
	Code             ErrorCode
	PasswordRequired bool
	ShareID          string
	SessionID        string
	SessionToken     string
	ExpiresAt        time.Time
	Generation       int
}

func (s *Service) Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error) {
	if s == nil || s.db == nil {
		return ExchangeResult{}, &ExchangeError{Code: CodeShareUnavailable, Err: errors.New("share service is not configured")}
	}

	publicID := strings.TrimSpace(req.PublicID)
	if publicID == "" || req.FragmentSecret == "" {
		return ExchangeResult{}, &ExchangeError{Code: CodeShareSecretMissing}
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}
	defer tx.Rollback()

	now := s.now().UTC()
	record, err := loadShareForExchange(ctx, tx, publicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExchangeResult{}, &ExchangeError{Code: CodeShareUnavailable}
		}
		return ExchangeResult{}, exchangeDBError(err)
	}
	if !record.availableAt(now) || !VerifySecret(req.FragmentSecret, record.secretHash) {
		return ExchangeResult{}, &ExchangeError{Code: CodeShareUnavailable}
	}

	if record.passwordHash.Valid {
		if req.Password == "" {
			return ExchangeResult{
				Code:             CodePasswordRequired,
				PasswordRequired: true,
				ShareID:          record.id,
			}, nil
		}
		if !VerifyPassword(req.Password, record.passwordHash.String) {
			return ExchangeResult{}, &ExchangeError{Code: CodeShareUnavailable}
		}
	}

	result, err := s.createSession(ctx, tx, record, now)
	if err != nil {
		return ExchangeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}
	return result, nil
}

type shareRecord struct {
	id           string
	secretHash   string
	passwordHash sql.NullString
	expiresAt    time.Time
	generation   int
	revokedAt    sql.NullString
}

func loadShareForExchange(ctx context.Context, tx *sql.Tx, publicID string) (shareRecord, error) {
	var record shareRecord
	var expiresAt string
	err := tx.QueryRowContext(ctx, `
SELECT id, secret_hash, password_hash, expires_at, generation, revoked_at
FROM shares
WHERE public_id = ?
`, publicID).Scan(
		&record.id,
		&record.secretHash,
		&record.passwordHash,
		&expiresAt,
		&record.generation,
		&record.revokedAt,
	)
	if err != nil {
		return shareRecord{}, err
	}

	parsed, err := parseSQLiteTime(expiresAt)
	if err != nil {
		return shareRecord{}, fmt.Errorf("parse share expiry: %w", err)
	}
	record.expiresAt = parsed
	return record, nil
}

func (r shareRecord) availableAt(now time.Time) bool {
	return !r.revokedAt.Valid && now.Before(r.expiresAt)
}

func (s *Service) createSession(ctx context.Context, tx *sql.Tx, record shareRecord, now time.Time) (ExchangeResult, error) {
	expiresAt := now.Add(s.sessionTTL)
	if record.expiresAt.Before(expiresAt) {
		expiresAt = record.expiresAt
	}
	if !now.Before(expiresAt) {
		return ExchangeResult{}, &ExchangeError{Code: CodeShareUnavailable}
	}

	update, err := tx.ExecContext(ctx, `
UPDATE shares
SET used_visits = used_visits + 1,
    updated_at = ?
WHERE id = ?
  AND revoked_at IS NULL
  AND expires_at > ?
  AND generation = ?
  AND (max_visits IS NULL OR used_visits < max_visits)
`, formatSQLiteTime(now), record.id, formatSQLiteTime(now), record.generation)
	if err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}
	if affected != 1 {
		return ExchangeResult{}, &ExchangeError{Code: CodeShareUnavailable}
	}

	sessionToken, err := NewSessionToken()
	if err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}
	sessionID, err := NewSecret()
	if err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO share_sessions(id, share_id, session_hash, generation, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
`, sessionID, record.id, HashSecret(sessionToken), record.generation, formatSQLiteTime(now), formatSQLiteTime(expiresAt))
	if err != nil {
		return ExchangeResult{}, exchangeDBError(err)
	}

	return ExchangeResult{
		ShareID:      record.id,
		SessionID:    sessionID,
		SessionToken: sessionToken,
		ExpiresAt:    expiresAt,
		Generation:   record.generation,
	}, nil
}

func parseSQLiteTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func formatSQLiteTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func exchangeDBError(err error) error {
	if isRetryableDBError(err) {
		return &ExchangeError{Code: CodeRateLimited, Err: err}
	}
	return &ExchangeError{Code: CodeShareUnavailable, Err: err}
}

func isRetryableDBError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "busy") || strings.Contains(message, "locked")
}
