package confirmation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"omnora/internal/aitoken"
)

const defaultTTL = 2 * time.Minute

var (
	// ErrInvalidInput is returned for malformed or incomplete challenge data.
	ErrInvalidInput = errors.New("confirmation: invalid input")
	// ErrUnavailable is deliberately indistinguishable for an unknown public
	// id, wrong secret, wrong principal, or a database lookup failure.
	ErrUnavailable = errors.New("confirmation: unavailable")
	ErrExpired     = errors.New("confirmation: expired")
	ErrDeclined    = errors.New("confirmation: declined")
	ErrConsumed    = errors.New("confirmation: already consumed")
)

// Service persists and consumes one-time confirmation challenges.
type Service struct {
	db  *sql.DB
	now func() time.Time
	ttl time.Duration
}

// Option configures a confirmation Service.
type Option func(*Service)

// WithClock injects a clock for deterministic expiry tests.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

// WithTTL changes the lifetime of newly-created challenges. Non-positive
// values are ignored so a service always has a bounded lifetime.
func WithTTL(ttl time.Duration) Option {
	return func(s *Service) {
		if ttl > 0 {
			s.ttl = ttl
		}
	}
}

// WithChallengeTTL is an explicit alias for callers that prefer a named
// challenge option while retaining the short WithTTL form.
func WithChallengeTTL(ttl time.Duration) Option {
	return WithTTL(ttl)
}

func NewService(db *sql.DB, opts ...Option) *Service {
	s := &Service{db: db, now: time.Now, ttl: defaultTTL}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Begin stores a challenge bound to the authenticated token, exact tool
// arguments, and the current object identity. Only the hash of the secret
// portion of RequestState is persisted.
func (s *Service) Begin(ctx context.Context, principal aitoken.Principal, preview Preview) (Challenge, error) {
	if s == nil || s.db == nil || !validPrincipal(principal) {
		return Challenge{}, ErrInvalidInput
	}
	if strings.TrimSpace(preview.ToolName) == "" || strings.TrimSpace(preview.ObjectFingerprint) == "" {
		return Challenge{}, ErrInvalidInput
	}
	canonicalArgs, err := canonicalJSON(preview.Args)
	if err != nil {
		return Challenge{}, ErrInvalidInput
	}
	impactJSON, err := json.Marshal(preview.Impact)
	if err != nil {
		return Challenge{}, ErrInvalidInput
	}

	secret, err := newSecret()
	if err != nil {
		return Challenge{}, ErrUnavailable
	}
	publicID, err := newID("mcpconf")
	if err != nil {
		return Challenge{}, ErrUnavailable
	}
	id, err := newID("mcpconfrow")
	if err != nil {
		return Challenge{}, ErrUnavailable
	}
	now := s.now().UTC().Round(0)
	expiresAt := now.Add(s.ttl)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO mcp_confirmations(
    id, public_id, secret_hash, account_id, ai_token_id, tool_name,
    args_hash, object_fingerprint, impact_json, status, created_at, expires_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
`, id, publicID, hashSecret(secret), principal.AccountID, principal.TokenID,
		preview.ToolName, hashArgs(canonicalArgs), preview.ObjectFingerprint, string(impactJSON),
		formatTime(now), formatTime(expiresAt))
	if err != nil {
		return Challenge{}, ErrUnavailable
	}
	return Challenge{
		RequestState: publicID + "." + secret,
		Impact:       preview.Impact,
		ExpiresAt:    expiresAt,
	}, nil
}

// Consume atomically transitions a pending challenge to accepted or declined.
// The caller must pass the exact operation preview again; any binding drift
// fails without changing the challenge state. A decline is persisted and
// returned as ErrDeclined so callers cannot accidentally execute the action.
func (s *Service) Consume(ctx context.Context, principal aitoken.Principal, req ConsumeRequest) error {
	if s == nil || s.db == nil || !validPrincipal(principal) {
		return ErrInvalidInput
	}
	publicID, secret, ok := parseRequestState(req.RequestState)
	if !ok {
		return ErrInvalidInput
	}
	if req.Decision != "accept" && req.Decision != "decline" {
		return ErrInvalidInput
	}
	if strings.TrimSpace(req.ToolName) == "" || strings.TrimSpace(req.ObjectFingerprint) == "" {
		return ErrInvalidInput
	}
	canonicalArgs, err := canonicalJSON(req.Args)
	if err != nil {
		return ErrInvalidInput
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()

	record, err := loadRecord(ctx, tx, publicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnavailable
		}
		return ErrUnavailable
	}
	if !secretMatches(secret, record.secretHash) ||
		record.accountID != principal.AccountID ||
		record.aiTokenID != principal.TokenID {
		return ErrUnavailable
	}
	if record.status != "pending" {
		if record.status == "expired" {
			return ErrExpired
		}
		return ErrConsumed
	}

	now := s.now().UTC().Round(0)
	expiresAt, err := parseTime(record.expiresAt)
	if err != nil {
		return ErrUnavailable
	}
	if !now.Before(expiresAt) {
		updated, updateErr := tx.ExecContext(ctx, `
UPDATE mcp_confirmations
SET status = 'expired', consumed_at = ?
WHERE id = ? AND status = 'pending'
`, formatTime(now), record.id)
		if updateErr != nil {
			return ErrUnavailable
		}
		if affected, affectedErr := updated.RowsAffected(); affectedErr != nil || affected != 1 {
			return ErrExpired
		}
		if err := tx.Commit(); err != nil {
			return ErrUnavailable
		}
		return ErrExpired
	}
	if record.toolName != req.ToolName ||
		record.argsHash != hashArgs(canonicalArgs) ||
		record.objectFingerprint != req.ObjectFingerprint {
		return ErrUnavailable
	}

	status := "accepted"
	if req.Decision == "decline" {
		status = "declined"
	}
	updated, err := tx.ExecContext(ctx, `
UPDATE mcp_confirmations
SET status = ?, consumed_at = ?
WHERE id = ?
  AND status = 'pending'
  AND expires_at > ?
  AND account_id = ?
  AND ai_token_id = ?
  AND tool_name = ?
  AND args_hash = ?
  AND object_fingerprint = ?
`, status, formatTime(now), record.id, formatTime(now), principal.AccountID,
		principal.TokenID, req.ToolName, record.argsHash, req.ObjectFingerprint)
	if err != nil {
		return ErrUnavailable
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if affected != 1 {
		return ErrConsumed
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	if status == "declined" {
		return ErrDeclined
	}
	return nil
}

type record struct {
	id                string
	secretHash        string
	accountID         string
	aiTokenID         string
	toolName          string
	argsHash          string
	objectFingerprint string
	status            string
	expiresAt         string
}

func loadRecord(ctx context.Context, tx *sql.Tx, publicID string) (record, error) {
	var item record
	err := tx.QueryRowContext(ctx, `
SELECT id, secret_hash, account_id, ai_token_id, tool_name, args_hash,
       object_fingerprint, status, expires_at
FROM mcp_confirmations
WHERE public_id = ?
`, publicID).Scan(
		&item.id, &item.secretHash, &item.accountID, &item.aiTokenID, &item.toolName,
		&item.argsHash, &item.objectFingerprint, &item.status, &item.expiresAt,
	)
	return item, err
}

func validPrincipal(principal aitoken.Principal) bool {
	return strings.TrimSpace(principal.AccountID) != "" &&
		strings.TrimSpace(principal.TokenID) != ""
}

func parseRequestState(value string) (string, string, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r\n") {
		return "", "", false
	}
	publicID, secret, ok := strings.Cut(value, ".")
	if !ok || publicID == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", false
	}
	return publicID, secret, true
}

func canonicalJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, ErrInvalidInput
		}
		return nil, err
	}
	return json.Marshal(value)
}

func hashArgs(canonical []byte) string {
	return hashBytes(canonical)
}

func hashSecret(secret string) string {
	return hashBytes([]byte(secret))
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func secretMatches(secret, encodedHash string) bool {
	want := hashSecret(secret)
	return subtle.ConstantTimeCompare([]byte(want), []byte(encodedHash)) == 1
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func newID(prefix string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	return prefix + "_" + secret, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Round(0).Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed, nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err = time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, err
}
