package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"omnora/internal/domain"
)

const timestampLayout = time.RFC3339Nano

type Service struct {
	db     *sql.DB
	clock  func() time.Time
	hasher PasswordHasher
}

type Options struct {
	Clock              func() time.Time
	PasswordIterations int
}

func New(db *sql.DB, opts Options) *Service {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		db:     db,
		clock:  clock,
		hasher: PasswordHasher{Iterations: opts.PasswordIterations},
	}
}

func InstallSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, SchemaSQL)
	return err
}

func (s *Service) PrepareInitialization(ctx context.Context, ttl time.Duration) (InitializationSecret, error) {
	if ttl <= 0 {
		return InitializationSecret{}, fieldError("ttl", "must be positive")
	}
	token, err := newOpaqueToken()
	if err != nil {
		return InitializationSecret{}, err
	}
	secret, err := s.prepareInitializationToken(ctx, token, ttl)
	if err != nil {
		return InitializationSecret{}, err
	}
	return secret, nil
}

func (s *Service) PrepareInitializationWithToken(ctx context.Context, token string, ttl time.Duration) (InitializationSecret, error) {
	if strings.TrimSpace(token) == "" {
		return InitializationSecret{}, fieldError("token", "is required")
	}
	if ttl <= 0 {
		return InitializationSecret{}, fieldError("ttl", "must be positive")
	}
	return s.prepareInitializationToken(ctx, token, ttl)
}

func (s *Service) prepareInitializationToken(ctx context.Context, token string, ttl time.Duration) (InitializationSecret, error) {
	expiresAt := s.now().Add(ttl)
	tokenHash := hashSecret(token)

	result, err := s.db.ExecContext(ctx, `
INSERT INTO identity_initialization (id, token_hash, expires_at)
SELECT 1, ?, ?
WHERE NOT EXISTS (SELECT 1 FROM accounts)
  AND NOT EXISTS (SELECT 1 FROM identity_initialization WHERE consumed_at IS NOT NULL)
ON CONFLICT(id) DO UPDATE SET
	token_hash = excluded.token_hash,
	expires_at = excluded.expires_at,
	consumed_at = NULL
WHERE identity_initialization.consumed_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM accounts)
`, tokenHash, formatTime(expiresAt))
	if err != nil {
		return InitializationSecret{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return InitializationSecret{}, err
	}
	if rows != 1 {
		return InitializationSecret{}, ErrAlreadyInitialized
	}
	return InitializationSecret{Token: token, TokenHash: tokenHash, ExpiresAt: expiresAt}, nil
}

func (s *Service) Initialize(ctx context.Context, req InitializationRequest) (AccountWithPersonalSpace, error) {
	return s.InitializeSecure(ctx, req, nil)
}

func (s *Service) InitializeSecure(ctx context.Context, req InitializationRequest, auditWriter AccountAuditWriter) (AccountWithPersonalSpace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	defer tx.Rollback()

	var accountCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(1) FROM accounts WHERE status <> 'deleted'").Scan(&accountCount); err != nil {
		return AccountWithPersonalSpace{}, err
	}
	if accountCount != 0 {
		return AccountWithPersonalSpace{}, ErrAlreadyInitialized
	}

	var tokenHash, expiresAtText string
	var consumedAt sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT token_hash, expires_at, consumed_at
FROM identity_initialization
WHERE id = 1
`).Scan(&tokenHash, &expiresAtText, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountWithPersonalSpace{}, ErrInitializationUnavailable
	}
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	expiresAt, err := parseTime(expiresAtText)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	now := s.now()
	if consumedAt.Valid || !now.Before(expiresAt) || !secretMatches(req.Token, tokenHash) {
		return AccountWithPersonalSpace{}, ErrInvalidCredential
	}

	result, err := tx.ExecContext(ctx, `
UPDATE identity_initialization
SET consumed_at = ?
WHERE id = 1
  AND consumed_at IS NULL
`, formatTime(now))
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	if rows != 1 {
		return AccountWithPersonalSpace{}, ErrInitializationUnavailable
	}

	created, err := s.createAccountTx(ctx, tx, CreateAccountRequest{
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
		Role:        domain.AccountRoleAdmin,
	})
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, created); err != nil {
			return AccountWithPersonalSpace{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AccountWithPersonalSpace{}, err
	}
	return created, nil
}

func (s *Service) CreateAccount(ctx context.Context, req CreateAccountRequest) (AccountWithPersonalSpace, error) {
	return s.CreateAccountSecure(ctx, req, nil)
}

// CreateAccountSecure creates the account and personal space and optionally
// records its success audit event before committing the same transaction.
func (s *Service) CreateAccountSecure(ctx context.Context, req CreateAccountRequest, auditWriter AccountAuditWriter) (AccountWithPersonalSpace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	defer tx.Rollback()

	created, err := s.createAccountTx(ctx, tx, req)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, created); err != nil {
			return AccountWithPersonalSpace{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AccountWithPersonalSpace{}, err
	}
	return created, nil
}

func (s *Service) CreateSession(ctx context.Context, req SessionRequest) (SessionToken, error) {
	if req.AccountID == "" {
		return SessionToken{}, fieldError("account_id", "is required")
	}
	if req.TTL <= 0 {
		return SessionToken{}, fieldError("ttl", "must be positive")
	}

	var active int
	var role string
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1), COALESCE(MAX(role), '')
FROM accounts
WHERE id = ? AND status = 'active'
`, req.AccountID).Scan(&active, &role); err != nil {
		return SessionToken{}, err
	}
	if active != 1 {
		return SessionToken{}, ErrInvalidCredential
	}

	token, err := newOpaqueToken()
	if err != nil {
		return SessionToken{}, err
	}
	now := s.now()
	sessionID, err := newID("ses")
	if err != nil {
		return SessionToken{}, err
	}
	entry := strings.TrimSpace(req.Entry)
	if entry == "" {
		entry = DefaultSessionEntry
	}
	purpose := req.Purpose
	if !purpose.Valid() {
		return SessionToken{}, fieldError("purpose", "is invalid")
	}
	if purpose == SessionPurposeTOTPEnrollment && role != "admin" {
		return SessionToken{}, fieldError("purpose", "totp enrollment is restricted to administrators")
	}
	var credentialGeneration int64
	if err := s.db.QueryRowContext(ctx, `
SELECT CAST(value AS INTEGER)
FROM system_state
WHERE key = 'credential_generation'
`).Scan(&credentialGeneration); err != nil {
		return SessionToken{}, err
	}
	session := Session{
		ID:                   sessionID,
		AccountID:            req.AccountID,
		TokenHash:            hashSecret(token),
		Entry:                entry,
		Purpose:              purpose,
		CredentialGeneration: credentialGeneration,
		CreatedAt:            now,
		ExpiresAt:            now.Add(req.TTL),
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO identity_sessions (id, account_id, token_hash, entry, purpose, credential_generation, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, session.ID, session.AccountID, session.TokenHash, session.Entry, session.Purpose, session.CredentialGeneration, formatTime(session.CreatedAt), formatTime(session.ExpiresAt))
	if err != nil {
		return SessionToken{}, err
	}
	return SessionToken{Token: token, Session: session}, nil
}

func (s *Service) Authenticate(ctx context.Context, email, password string) (Account, error) {
	normalizedEmail, err := normalizeEmail(email)
	if err != nil || password == "" {
		return Account{}, ErrInvalidCredential
	}

	var account Account
	var createdAt, updatedAt string
	var totpRequired, passwordResetRequired, totpResetRequired int
	var totpConfirmedAt sql.NullString
	err = s.db.QueryRowContext(ctx, `
SELECT id, email, display_name, role, status, password_hash,
       totp_required, totp_confirmed_at, password_reset_required, totp_reset_required,
       created_at, updated_at
FROM accounts
WHERE email = ? AND status = 'active'
`, normalizedEmail).Scan(
		&account.ID,
		&account.Email,
		&account.DisplayName,
		&account.Role,
		&account.Status,
		&account.PasswordHash,
		&totpRequired,
		&totpConfirmedAt,
		&passwordResetRequired,
		&totpResetRequired,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrInvalidCredential
	}
	if err != nil {
		return Account{}, err
	}
	account.TOTPRequired = totpRequired == 1
	account.PasswordResetRequired = passwordResetRequired == 1
	account.TOTPResetRequired = totpResetRequired == 1
	if totpConfirmedAt.Valid {
		account.TOTPConfirmedAt, err = parseTime(totpConfirmedAt.String)
		if err != nil {
			return Account{}, err
		}
	}
	if !s.hasher.Verify(password, account.PasswordHash) {
		return Account{}, ErrInvalidCredential
	}
	account.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Account{}, err
	}
	account.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Account{}, err
	}
	return account, nil
}

func (s *Service) VerifySession(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionInvalid
	}
	tokenHash := hashSecret(token)
	now := s.now()

	var session Session
	var createdAt, expiresAt string
	var lastUsedAt, reauthenticatedAt, revokedAt sql.NullString
	var purpose string
	var credentialGeneration int64
	err := s.db.QueryRowContext(ctx, `
SELECT s.id, s.account_id, s.token_hash, s.entry, s.purpose, s.credential_generation,
       s.created_at, s.expires_at, s.last_used_at, s.reauthenticated_at, s.revoked_at
FROM identity_sessions s
JOIN accounts a ON a.id = s.account_id
WHERE s.token_hash = ?
  AND s.revoked_at IS NULL
  AND s.expires_at > ?
  AND a.status = 'active'
  AND s.purpose IN ('full', 'totp_enrollment')
  AND s.credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
	`, tokenHash, formatTime(now)).Scan(
		&session.ID,
		&session.AccountID,
		&session.TokenHash,
		&session.Entry,
		&purpose,
		&credentialGeneration,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
		&reauthenticatedAt,
		&revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	if err != nil {
		return Session{}, err
	}
	session.Purpose = SessionPurpose(purpose)
	session.CredentialGeneration = credentialGeneration
	session.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Session{}, err
	}
	session.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return Session{}, err
	}
	if !now.Before(session.ExpiresAt) {
		return Session{}, ErrSessionInvalid
	}
	if lastUsedAt.Valid {
		session.LastUsedAt, err = parseTime(lastUsedAt.String)
		if err != nil {
			return Session{}, err
		}
	}
	if reauthenticatedAt.Valid {
		session.ReauthenticatedAt, err = parseTime(reauthenticatedAt.String)
		if err != nil {
			return Session{}, err
		}
	}
	if revokedAt.Valid {
		session.RevokedAt, err = parseTime(revokedAt.String)
		if err != nil {
			return Session{}, err
		}
	}

	if _, err := s.db.ExecContext(ctx, `
UPDATE identity_sessions
SET last_used_at = ?
WHERE id = ? AND revoked_at IS NULL AND expires_at > ?
`, formatTime(now), session.ID, formatTime(now)); err != nil {
		return Session{}, err
	}
	session.LastUsedAt = now
	return session, nil
}

func (s *Service) RevokeSession(ctx context.Context, token string) error {
	if token == "" {
		return ErrSessionInvalid
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE token_hash = ? AND revoked_at IS NULL
`, formatTime(s.now()), hashSecret(token))
	return err
}

func (s *Service) VerifyPassword(password, encodedHash string) bool {
	return s.hasher.Verify(password, encodedHash)
}

func (s *Service) createAccountTx(ctx context.Context, tx *sql.Tx, req CreateAccountRequest) (AccountWithPersonalSpace, error) {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	displayName, err := normalizeDisplayName(req.DisplayName)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	if err := validatePassword(req.Password); err != nil {
		return AccountWithPersonalSpace{}, err
	}
	role, err := normalizeRole(req.Role)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	passwordHash, err := s.hasher.Hash(req.Password)
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}

	now := s.now()
	accountID, err := newID("acct")
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	account := Account{
		ID:           accountID,
		Email:        email,
		DisplayName:  displayName,
		Role:         role,
		Status:       "active",
		PasswordHash: passwordHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO accounts (id, email, display_name, role, status, password_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, account.ID, account.Email, account.DisplayName, account.Role, account.Status, account.PasswordHash, formatTime(now), formatTime(now))
	if err != nil {
		if isUniqueViolation(err) {
			return AccountWithPersonalSpace{}, ErrAccountExists
		}
		return AccountWithPersonalSpace{}, err
	}

	spaceID, err := newID("spc")
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	space := Space{
		ID:             spaceID,
		Kind:           "personal",
		Name:           displayName + "'s space",
		OwnerAccountID: account.ID,
		Status:         "active",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO spaces (id, kind, name, owner_account_id, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, space.ID, space.Kind, space.Name, space.OwnerAccountID, space.Status, formatTime(now), formatTime(now))
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO space_members (space_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
`, space.ID, account.ID, domain.SpacePermissionManager, formatTime(now), formatTime(now))
	if err != nil {
		return AccountWithPersonalSpace{}, err
	}

	return AccountWithPersonalSpace{Account: account, PersonalSpace: space}, nil
}

func (s *Service) now() time.Time {
	return s.clock().UTC().Round(0)
}

func newID(prefix string) (string, error) {
	token, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	return prefix + "_" + token, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(timestampLayout)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(timestampLayout, value)
}

func isUniqueViolation(err error) bool {
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed")
}
