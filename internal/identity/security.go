package identity

import (
	"context"
	"database/sql"
	"errors"

	"omnora/internal/domain"
)

// ErrSessionNotFound is returned when a session id does not belong to the
// requesting account or is already revoked/expired.
var ErrSessionNotFound = errors.New("identity: session not found")

// TokenHash exposes the session token hashing function so HTTP handlers can
// compare an incoming session cookie against stored session summaries
// without ever persisting or logging the raw token.
func (s *Service) TokenHash(token string) string {
	return hashSecret(token)
}

// ChangePassword verifies the current password before rotating the stored
// password hash. It does not revoke sessions; callers decide whether to also
// revoke sessions or shares.
func (s *Service) ChangePassword(ctx context.Context, accountID, currentPassword, newPassword string) error {
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `
SELECT password_hash
FROM accounts
WHERE id = ? AND status = 'active'
`, accountID).Scan(&passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredential
	}
	if err != nil {
		return err
	}
	if !s.hasher.Verify(currentPassword, passwordHash) {
		return ErrInvalidCredential
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	newHash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE accounts
SET password_hash = ?, password_reset_required = 0, updated_at = ?
WHERE id = ? AND status = 'active'
`, newHash, formatTime(s.now()), accountID)
	return err
}

// ListSessions returns all non-revoked, non-expired sessions for an account,
// most recent first. The TokenHash field is included so HTTP handlers can
// determine which session is "current" without exposing raw tokens.
func (s *Service) ListSessions(ctx context.Context, accountID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, account_id, token_hash, entry, purpose, credential_generation,
	       created_at, expires_at, last_used_at, reauthenticated_at, revoked_at
	FROM identity_sessions
	WHERE account_id = ? AND revoked_at IS NULL AND expires_at > ?
	  AND purpose IN ('full', 'totp_enrollment')
	  AND credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
ORDER BY created_at DESC
`, accountID, formatTime(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var purpose string
		var createdAt, expiresAt string
		var lastUsedAt, reauthenticatedAt, revokedAt sql.NullString
		if err := rows.Scan(&session.ID, &session.AccountID, &session.TokenHash, &session.Entry, &purpose, &session.CredentialGeneration, &createdAt, &expiresAt, &lastUsedAt, &reauthenticatedAt, &revokedAt); err != nil {
			return nil, err
		}
		session.Purpose = SessionPurpose(purpose)
		session.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		session.ExpiresAt, err = parseTime(expiresAt)
		if err != nil {
			return nil, err
		}
		if lastUsedAt.Valid {
			session.LastUsedAt, err = parseTime(lastUsedAt.String)
			if err != nil {
				return nil, err
			}
		}
		if reauthenticatedAt.Valid {
			session.ReauthenticatedAt, err = parseTime(reauthenticatedAt.String)
			if err != nil {
				return nil, err
			}
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// RevokeSessionByID revokes one session owned by accountID.
func (s *Service) RevokeSessionByID(ctx context.Context, accountID, sessionID string) error {
	if accountID == "" || sessionID == "" {
		return ErrSessionNotFound
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE id = ? AND account_id = ? AND revoked_at IS NULL
`, formatTime(s.now()), sessionID, accountID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeAllSessions revokes every active session for accountID except the
// session identified by keepSessionID (pass an empty string to revoke all).
func (s *Service) RevokeAllSessions(ctx context.Context, accountID, keepSessionID string) error {
	return s.RevokeAllSessionsSecure(ctx, accountID, keepSessionID, nil)
}

// RevokeAllSessionsSecure fences every active session and optionally writes
// the success audit row in the same transaction.
func (s *Service) RevokeAllSessionsSecure(ctx context.Context, accountID, keepSessionID string, auditWriter TxAuditWriter) error {
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE account_id = ? AND revoked_at IS NULL AND id <> ?
`, formatTime(s.now()), accountID, keepSessionID); err != nil {
		return err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DisableTOTP clears TOTP enrollment for an account so login no longer
// requires a code.
func (s *Service) DisableTOTP(ctx context.Context, accountID string) error {
	return s.DisableTOTPSecure(ctx, accountID, nil)
}

// DisableTOTPSecure clears TOTP and writes an optional success audit event in
// the same transaction. The role/policy decision remains at the HTTP boundary
// so password and code verification stay outside the write transaction.
func (s *Service) DisableTOTPSecure(ctx context.Context, accountID string, auditWriter TxAuditWriter) error {
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var role string
	if err := tx.QueryRowContext(ctx, `
SELECT role
FROM accounts
WHERE id = ? AND status = 'active'
`, accountID).Scan(&role); errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidCredential
	} else if err != nil {
		return err
	} else if role == string(domain.AccountRoleAdmin) {
		return ErrAdminTOTPRequired
	}
	result, err := tx.ExecContext(ctx, `
UPDATE accounts
SET totp_required = 0,
    totp_secret_ciphertext = NULL,
    totp_confirmed_at = NULL,
    totp_pending_secret_ciphertext = NULL,
    totp_pending_expires_at = NULL,
    updated_at = ?
WHERE id = ? AND status = 'active'
`, formatTime(s.now()), accountID)
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
