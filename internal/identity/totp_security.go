package identity

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PendingTOTPDuration is the maximum time an unconfirmed authenticator
// secret remains eligible for confirmation.
const PendingTOTPDuration = 10 * time.Minute

type TOTPState struct {
	Required          bool
	ActiveCiphertext  string
	PendingCiphertext string
	PendingExpiresAt  time.Time
}

// LoadTOTPState returns the encrypted active and pending authenticator state.
// Decryption and code verification remain in the HTTP layer so this service
// never handles plaintext TOTP secrets.
func (s *Service) LoadTOTPState(ctx context.Context, accountID string) (TOTPState, error) {
	if accountID == "" {
		return TOTPState{}, fieldError("account_id", "is required")
	}
	var required int
	var activeCiphertext, pendingCiphertext, pendingExpiresAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT totp_required, totp_secret_ciphertext, totp_pending_secret_ciphertext, totp_pending_expires_at
FROM accounts
WHERE id = ? AND status = 'active'
`, accountID).Scan(&required, &activeCiphertext, &pendingCiphertext, &pendingExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TOTPState{}, ErrInvalidCredential
	}
	if err != nil {
		return TOTPState{}, err
	}
	state := TOTPState{Required: required == 1}
	if activeCiphertext.Valid {
		state.ActiveCiphertext = activeCiphertext.String
	}
	if pendingCiphertext.Valid {
		state.PendingCiphertext = pendingCiphertext.String
	}
	if pendingExpiresAt.Valid && pendingExpiresAt.String != "" {
		state.PendingExpiresAt, err = parseTime(pendingExpiresAt.String)
		if err != nil {
			return TOTPState{}, err
		}
	}
	return state, nil
}

// SavePendingTOTP replaces only the pending authenticator secret. The active
// confirmed secret is intentionally untouched until PromotePendingTOTP.
func (s *Service) SavePendingTOTP(ctx context.Context, accountID, ciphertext string, expiresAt time.Time) error {
	return s.SavePendingTOTPSecure(ctx, accountID, ciphertext, expiresAt, nil)
}

// SavePendingTOTPSecure writes pending enrollment material and optionally
// records its success audit row in the same transaction.
func (s *Service) SavePendingTOTPSecure(ctx context.Context, accountID, ciphertext string, expiresAt time.Time, auditWriter TxAuditWriter) error {
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	if ciphertext == "" {
		return fieldError("ciphertext", "is required")
	}
	expiresAt = expiresAt.UTC().Round(0)
	if !expiresAt.After(s.now()) {
		return fieldError("expires_at", "must be in the future")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE accounts
SET totp_pending_secret_ciphertext = ?, totp_pending_expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'active'
`, ciphertext, formatTime(expiresAt), formatTime(s.now()), accountID)
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
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PromotePendingTOTP atomically promotes the expected, unexpired pending
// secret to active and enables TOTP. A stale or concurrently replaced pending
// secret cannot be promoted.
func (s *Service) PromotePendingTOTP(ctx context.Context, accountID, expectedCiphertext string, now time.Time) error {
	_, err := s.promotePendingTOTP(ctx, accountID, expectedCiphertext, now, Session{}, nil)
	return err
}

// PromotePendingTOTPAndRotateSession promotes the pending secret, revokes every
// identity session for the account (including the caller), and issues a new
// full-purpose session that preserves the absolute expiry. The replacement
// token is returned only after commit so callers can rotate cookies/CSRF.
func (s *Service) PromotePendingTOTPAndRotateSession(ctx context.Context, current Session, expectedCiphertext string, now time.Time) (SessionToken, error) {
	return s.promotePendingTOTP(ctx, current.AccountID, expectedCiphertext, now, current, nil)
}

// PromotePendingTOTPAndRotateSessionSecure adds the success-audit hook to the
// same transaction as the promotion and session rotation.
func (s *Service) PromotePendingTOTPAndRotateSessionSecure(ctx context.Context, current Session, expectedCiphertext string, now time.Time, auditWriter TxAuditWriter) (SessionToken, error) {
	return s.promotePendingTOTP(ctx, current.AccountID, expectedCiphertext, now, current, auditWriter)
}

// PromotePendingTOTPAndRevokeOtherSessions is retained for older call sites and
// delegates to the rotating confirm path.
func (s *Service) PromotePendingTOTPAndRevokeOtherSessions(ctx context.Context, accountID, keepSessionID, expectedCiphertext string, now time.Time) error {
	current := Session{ID: keepSessionID, AccountID: accountID}
	var expiresAt string
	err := s.db.QueryRowContext(ctx, `
SELECT token_hash, entry, expires_at FROM identity_sessions
WHERE id = ? AND account_id = ? AND revoked_at IS NULL
`, keepSessionID, accountID).Scan(&current.TokenHash, &current.Entry, &expiresAt)
	if err != nil {
		return ErrSessionInvalid
	}
	current.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return err
	}
	_, err = s.promotePendingTOTP(ctx, accountID, expectedCiphertext, now, current, nil)
	return err
}

// PromotePendingTOTPAndRevokeOtherSessionsSecure delegates to the rotating
// confirm path while preserving the previous method signature for callers that
// ignore the replacement token.
func (s *Service) PromotePendingTOTPAndRevokeOtherSessionsSecure(ctx context.Context, accountID, keepSessionID, expectedCiphertext string, now time.Time, auditWriter TxAuditWriter) error {
	current := Session{ID: keepSessionID, AccountID: accountID}
	var expiresAt string
	err := s.db.QueryRowContext(ctx, `
SELECT token_hash, entry, expires_at FROM identity_sessions
WHERE id = ? AND account_id = ? AND revoked_at IS NULL
`, keepSessionID, accountID).Scan(&current.TokenHash, &current.Entry, &expiresAt)
	if err != nil {
		return ErrSessionInvalid
	}
	current.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return err
	}
	_, err = s.promotePendingTOTP(ctx, accountID, expectedCiphertext, now, current, auditWriter)
	return err
}

func (s *Service) promotePendingTOTP(ctx context.Context, accountID, expectedCiphertext string, now time.Time, current Session, auditWriter TxAuditWriter) (SessionToken, error) {
	if accountID == "" || expectedCiphertext == "" {
		return SessionToken{}, ErrInvalidCredential
	}
	now = now.UTC().Round(0)
	rotate := current.ID != ""
	var (
		token      string
		newSession Session
		err        error
	)
	if rotate {
		if current.AccountID != accountID || current.TokenHash == "" || current.ExpiresAt.IsZero() || !now.Before(current.ExpiresAt) {
			return SessionToken{}, ErrSessionInvalid
		}
		token, err = newOpaqueToken()
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
		entry := current.Entry
		if entry == "" {
			entry = DefaultSessionEntry
		}
		newSession = Session{
			ID:                   newSessionID,
			AccountID:            accountID,
			TokenHash:            hashSecret(token),
			Entry:                entry,
			Purpose:              SessionPurposeFull,
			CredentialGeneration: credentialGeneration,
			CreatedAt:            now,
			ExpiresAt:            current.ExpiresAt,
			LastUsedAt:           now,
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionToken{}, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
UPDATE accounts
SET totp_secret_ciphertext = totp_pending_secret_ciphertext,
    totp_required = 1,
    totp_confirmed_at = ?,
    totp_reset_required = 0,
    totp_pending_secret_ciphertext = NULL,
    totp_pending_expires_at = NULL,
    updated_at = ?
WHERE id = ? AND status = 'active'
  AND totp_pending_secret_ciphertext = ?
  AND totp_pending_expires_at > ?
`, formatTime(now), formatTime(now), accountID, expectedCiphertext, formatTime(now))
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

	if rotate {
		if _, err := tx.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE account_id = ? AND revoked_at IS NULL
`, formatTime(now), accountID); err != nil {
			return SessionToken{}, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO identity_sessions
	(id, account_id, token_hash, entry, purpose, credential_generation, created_at, expires_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, newSession.ID, newSession.AccountID, newSession.TokenHash, newSession.Entry, newSession.Purpose,
			newSession.CredentialGeneration, formatTime(newSession.CreatedAt), formatTime(newSession.ExpiresAt), formatTime(newSession.LastUsedAt)); err != nil {
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
	if !rotate {
		return SessionToken{}, nil
	}
	return SessionToken{Token: token, Session: newSession}, nil
}
