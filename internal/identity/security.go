package identity

import (
	"context"
	"database/sql"
	"errors"
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
SET password_hash = ?, updated_at = ?
WHERE id = ? AND status = 'active'
`, newHash, formatTime(s.now()), accountID)
	return err
}

// ListSessions returns all non-revoked, non-expired sessions for an account,
// most recent first. The TokenHash field is included so HTTP handlers can
// determine which session is "current" without exposing raw tokens.
func (s *Service) ListSessions(ctx context.Context, accountID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, account_id, token_hash, entry, created_at, expires_at, last_used_at, revoked_at
FROM identity_sessions
WHERE account_id = ? AND revoked_at IS NULL AND expires_at > ?
ORDER BY created_at DESC
`, accountID, formatTime(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var createdAt, expiresAt string
		var lastUsedAt, revokedAt sql.NullString
		if err := rows.Scan(&session.ID, &session.AccountID, &session.TokenHash, &session.Entry, &createdAt, &expiresAt, &lastUsedAt, &revokedAt); err != nil {
			return nil, err
		}
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
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE identity_sessions
SET revoked_at = ?
WHERE account_id = ? AND revoked_at IS NULL AND id <> ?
`, formatTime(s.now()), accountID, keepSessionID)
	return err
}

// DisableTOTP clears TOTP enrollment for an account so login no longer
// requires a code.
func (s *Service) DisableTOTP(ctx context.Context, accountID string) error {
	if accountID == "" {
		return fieldError("account_id", "is required")
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE accounts
SET totp_required = 0, totp_secret_ciphertext = NULL, totp_confirmed_at = NULL, updated_at = ?
WHERE id = ? AND status = 'active'
`, formatTime(s.now()), accountID)
	return err
}
