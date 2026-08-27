package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"omnora/internal/domain"
)

// CredentialMaterial contains only the encrypted TOTP material needed by an
// HTTP boundary to verify a recent reauthentication. Plaintext secrets never
// enter the identity service.
type CredentialMaterial struct {
	AccountID            string
	Role                 domain.AccountRole
	Status               string
	PasswordHash         string
	TOTPRequired         bool
	TOTPSecretCiphertext string
}

// ChangePasswordSecureRequest describes a password mutation after the
// expensive password hash has been computed outside the write transaction.
type ChangePasswordSecureRequest struct {
	Session         Session
	ExpectedOldHash string
	NewPasswordHash string
	RevokeTokens    bool
	RevokeShares    bool
}

// TxAuditWriter is called while the secure mutation transaction is open. A
// caller can use it to insert the success audit row in the same commit.
type TxAuditWriter func(context.Context, *sql.Tx) error

// AccountAuditWriter receives the account created inside the open transaction
// so callers can bind the audit target without querying through *sql.DB.
type AccountAuditWriter func(context.Context, *sql.Tx, AccountWithPersonalDirectory) error

// LoadCredentialMaterial reads the current credential state for an active
// account. Callers must perform password/TOTP verification before starting a
// write transaction.
func (s *Service) LoadCredentialMaterial(ctx context.Context, accountID string) (CredentialMaterial, error) {
	if strings.TrimSpace(accountID) == "" {
		return CredentialMaterial{}, fieldError("account_id", "is required")
	}
	var material CredentialMaterial
	var required int
	var ciphertext sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, role, status, password_hash, totp_required, totp_secret_ciphertext
FROM accounts
WHERE id = ? AND status = 'active'
`, accountID).Scan(
		&material.AccountID,
		&material.Role,
		&material.Status,
		&material.PasswordHash,
		&required,
		&ciphertext,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialMaterial{}, ErrInvalidCredential
	}
	if err != nil {
		return CredentialMaterial{}, err
	}
	material.TOTPRequired = required == 1
	if ciphertext.Valid {
		material.TOTPSecretCiphertext = ciphertext.String
	}
	return material, nil
}

// HashPassword validates and hashes a password before a secure mutation
// begins. Keeping the expensive work outside SQLite's write transaction
// prevents password changes from blocking unrelated requests.
func (s *Service) HashPassword(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	return s.hasher.Hash(password)
}

// ChangePasswordSecure updates the password, revokes every other session, and
// rotates the caller's current session so a leaked cookie copy cannot survive
// the password change. An optional audit writer runs before commit.
func (s *Service) ChangePasswordSecure(ctx context.Context, req ChangePasswordSecureRequest, auditWriter TxAuditWriter) (SessionToken, error) {
	if req.Session.ID == "" || req.Session.AccountID == "" || req.Session.Purpose != SessionPurposeFull {
		return SessionToken{}, ErrSessionInvalid
	}
	if strings.TrimSpace(req.ExpectedOldHash) == "" || strings.TrimSpace(req.NewPasswordHash) == "" {
		return SessionToken{}, fieldError("password", "is required")
	}
	now := s.now()
	if !now.Before(req.Session.ExpiresAt) {
		return SessionToken{}, ErrSessionInvalid
	}
	token, err := newOpaqueToken()
	if err != nil {
		return SessionToken{}, err
	}
	newSessionID, err := newID("ses")
	if err != nil {
		return SessionToken{}, err
	}
	credentialGeneration, err := s.currentCredentialGeneration(ctx)
	if err != nil {
		return SessionToken{}, err
	}
	reauthenticatedAt := req.Session.ReauthenticatedAt
	if reauthenticatedAt.IsZero() || now.Sub(reauthenticatedAt) > RecentReauthenticationTTL {
		reauthenticatedAt = now
	}
	newSession := Session{
		ID:                   newSessionID,
		AccountID:            req.Session.AccountID,
		TokenHash:            hashSecret(token),
		Entry:                req.Session.Entry,
		Purpose:              SessionPurposeFull,
		ReauthenticatedAt:    reauthenticatedAt,
		CredentialGeneration: credentialGeneration,
		CreatedAt:            now,
		ExpiresAt:            req.Session.ExpiresAt,
		LastUsedAt:           now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionToken{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE accounts
SET password_hash = ?, password_reset_required = 0, updated_at = ?
WHERE id = ? AND status = 'active' AND password_hash = ?
`, req.NewPasswordHash, formatTime(now), req.Session.AccountID, req.ExpectedOldHash)
	if err != nil {
		return SessionToken{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SessionToken{}, err
	}
	if affected != 1 {
		return SessionToken{}, ErrInvalidCredential
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE account_id = ? AND revoked_at IS NULL
`, formatTime(now), req.Session.AccountID); err != nil {
		return SessionToken{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO identity_sessions
	(id, account_id, token_hash, entry, purpose, reauthenticated_at, credential_generation, created_at, expires_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, newSession.ID, newSession.AccountID, newSession.TokenHash, newSession.Entry, newSession.Purpose,
		formatTime(newSession.ReauthenticatedAt), newSession.CredentialGeneration, formatTime(newSession.CreatedAt),
		formatTime(newSession.ExpiresAt), formatTime(newSession.LastUsedAt)); err != nil {
		return SessionToken{}, err
	}
	if req.RevokeTokens {
		if _, err := tx.ExecContext(ctx, `
UPDATE ai_tokens
SET revoked_at = ?, updated_at = ?
WHERE account_id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), req.Session.AccountID); err != nil {
			return SessionToken{}, err
		}
	}
	if req.RevokeShares {
		if _, err := tx.ExecContext(ctx, `
UPDATE shares
SET revoked_at = ?, invalidated_at = ?, invalidated_reason = 'password_change',
    credential_generation = COALESCE(credential_generation, CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)),
    generation = generation + 1, updated_at = ?
WHERE creator_account_id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), formatTime(now), req.Session.AccountID); err != nil {
			return SessionToken{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE share_sessions
SET revoked_at = ?
WHERE share_id IN (SELECT id FROM shares WHERE creator_account_id = ?)
  AND revoked_at IS NULL
`, formatTime(now), req.Session.AccountID); err != nil {
			return SessionToken{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE share_download_tickets
SET status = 'canceled', canceled_at = ?
WHERE share_id IN (SELECT id FROM shares WHERE creator_account_id = ?)
  AND status IN ('issued', 'streaming')
`, formatTime(now), req.Session.AccountID); err != nil {
			return SessionToken{}, err
		}
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return SessionToken{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SessionToken{}, err
	}
	return SessionToken{Token: token, Session: newSession}, nil
}

// DisableAccountSecure changes an account to disabled and revokes every
// credential class that can outlive an HTTP session. Re-enabling is purposely
// not represented here: it must only change account status and never clear
// these revocation markers.
func (s *Service) DisableAccountSecure(ctx context.Context, accountID string, auditWriter TxAuditWriter) error {
	if strings.TrimSpace(accountID) == "" {
		return fieldError("account_id", "is required")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE accounts SET status = 'disabled', updated_at = ?
WHERE id = ? AND status = 'active'
`, formatTime(now), accountID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInvalidCredential
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE identity_sessions SET revoked_at = ?
WHERE account_id = ? AND revoked_at IS NULL
`, formatTime(now), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE browser_sessions SET revoked_at = ?
WHERE account_id = ? AND revoked_at IS NULL
`, formatTime(now), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE ai_tokens SET revoked_at = ?, updated_at = ?
WHERE account_id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE shares
SET revoked_at = ?, invalidated_at = ?, invalidated_reason = 'account_disabled',
    credential_generation = COALESCE(credential_generation, CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)),
    generation = generation + 1, updated_at = ?
WHERE creator_account_id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), formatTime(now), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE share_sessions SET revoked_at = ?
WHERE share_id IN (SELECT id FROM shares WHERE creator_account_id = ?)
  AND revoked_at IS NULL
`, formatTime(now), accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE share_download_tickets
SET status = 'canceled', canceled_at = ?
WHERE share_id IN (SELECT id FROM shares WHERE creator_account_id = ?)
  AND status IN ('issued', 'streaming')
`, formatTime(now), accountID); err != nil {
		return err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAccountSecure irreversibly tombstones an identity while preserving its
// personal directory binding and every physical file. All account-scoped
// credentials and live content grants are revoked in the same DB transaction.
func (s *Service) DeleteAccountSecure(ctx context.Context, accountID string, auditWriter TxAuditWriter) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	now := formatTime(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id = ?`, accountID).Scan(&status); err != nil || status == "deleted" {
		return ErrInvalidCredential
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE identity_sessions SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL`, []any{now, accountID}},
		{`UPDATE browser_sessions SET revoked_at = ? WHERE account_id = ? AND revoked_at IS NULL`, []any{now, accountID}},
		{`UPDATE ai_tokens SET revoked_at = ?, updated_at = ? WHERE account_id = ? AND revoked_at IS NULL`, []any{now, now, accountID}},
		{`UPDATE shares SET revoked_at = ?, invalidated_at = ?, invalidated_reason = 'account_deleted', generation = generation + 1, updated_at = ? WHERE creator_account_id = ? AND revoked_at IS NULL`, []any{now, now, now, accountID}},
		{`UPDATE share_sessions SET revoked_at = ? WHERE share_id IN (SELECT id FROM shares WHERE creator_account_id = ?) AND revoked_at IS NULL`, []any{now, accountID}},
		{`UPDATE share_download_tickets SET status = 'canceled', canceled_at = ? WHERE share_id IN (SELECT id FROM shares WHERE creator_account_id = ?) AND status IN ('issued', 'streaming')`, []any{now, accountID}},
		{`UPDATE folder_collaborations SET revoked_at = ?, updated_at = ? WHERE (owner_account_id = ? OR recipient_account_id = ?) AND revoked_at IS NULL`, []any{now, now, accountID, accountID}},
		{`DELETE FROM mount_grants WHERE account_id = ?`, []any{accountID}},
		{`UPDATE upload_sessions SET status = 'canceled', canceled_at = ? WHERE account_id = ? AND status = 'active'`, []any{now, accountID}},
		{`UPDATE mcp_transfer_tickets SET status = 'canceled', closed_at = ? WHERE account_id = ? AND status = 'active'`, []any{now, accountID}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return err
		}
	}
	if err := s.personalStorage.RetainAccount(ctx, tx, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// RotateSession atomically revokes the current full session and issues a new
// session with the same absolute expiry. Reauthentication is recorded on the
// replacement row and cannot be used to extend the original lifetime.
func (s *Service) RotateSession(ctx context.Context, session Session, reauthenticatedAt time.Time) (SessionToken, error) {
	return s.RotateSessionSecure(ctx, session, reauthenticatedAt, nil)
}

// RotateSessionSecure rotates a session and optionally records the success
// audit event in the same transaction. The replacement token is returned only
// after that transaction commits.
func (s *Service) RotateSessionSecure(ctx context.Context, session Session, reauthenticatedAt time.Time, auditWriter TxAuditWriter) (SessionToken, error) {
	if session.ID == "" || session.AccountID == "" || session.TokenHash == "" {
		return SessionToken{}, ErrSessionInvalid
	}
	if session.Purpose != SessionPurposeFull {
		return SessionToken{}, ErrEnrollmentSession
	}
	now := s.now()
	reauthenticatedAt = reauthenticatedAt.UTC().Round(0)
	if reauthenticatedAt.IsZero() || reauthenticatedAt.After(now.Add(time.Second)) {
		return SessionToken{}, fieldError("reauthenticated_at", "is invalid")
	}
	if !now.Before(session.ExpiresAt) {
		return SessionToken{}, ErrSessionInvalid
	}
	token, err := newOpaqueToken()
	if err != nil {
		return SessionToken{}, err
	}
	newSessionID, err := newID("ses")
	if err != nil {
		return SessionToken{}, err
	}
	credentialGeneration, err := s.currentCredentialGeneration(ctx)
	if err != nil {
		return SessionToken{}, err
	}
	newSession := Session{
		ID:                   newSessionID,
		AccountID:            session.AccountID,
		TokenHash:            hashSecret(token),
		Entry:                session.Entry,
		Purpose:              SessionPurposeFull,
		ReauthenticatedAt:    reauthenticatedAt,
		CredentialGeneration: credentialGeneration,
		CreatedAt:            now,
		ExpiresAt:            session.ExpiresAt,
		LastUsedAt:           now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionToken{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE id = ? AND account_id = ? AND token_hash = ?
  AND revoked_at IS NULL AND expires_at > ?
  AND credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
`, formatTime(now), session.ID, session.AccountID, session.TokenHash, formatTime(now))
	if err != nil {
		return SessionToken{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return SessionToken{}, err
	}
	if affected != 1 {
		return SessionToken{}, ErrSessionInvalid
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO identity_sessions
	(id, account_id, token_hash, entry, purpose, reauthenticated_at, credential_generation, created_at, expires_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, newSession.ID, newSession.AccountID, newSession.TokenHash, newSession.Entry, newSession.Purpose,
		formatTime(newSession.ReauthenticatedAt), newSession.CredentialGeneration, formatTime(newSession.CreatedAt),
		formatTime(newSession.ExpiresAt), formatTime(newSession.LastUsedAt)); err != nil {
		return SessionToken{}, err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return SessionToken{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SessionToken{}, err
	}
	return SessionToken{Token: token, Session: newSession}, nil
}

func (s *Service) currentCredentialGeneration(ctx context.Context) (int64, error) {
	var generation int64
	if err := s.db.QueryRowContext(ctx, `
SELECT CAST(value AS INTEGER) FROM system_state WHERE key = 'credential_generation'
`).Scan(&generation); err != nil {
		return 0, err
	}
	return generation, nil
}
