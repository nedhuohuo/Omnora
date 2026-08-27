package identity

import (
	"context"
	"database/sql"
	"time"
)

// PrepareSessionPurposeRollout backfills legacy sessions before the HTTP
// listener starts. Unknown purposes block startup rather than being silently
// reinterpreted. Active, unexpired legacy sessions become full. TOTP is
// optional for administrators, so sessions are not revoked for missing TOTP.
func PrepareSessionPurposeRollout(ctx context.Context, db *sql.DB, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var invalid int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM identity_sessions
WHERE revoked_at IS NULL
  AND expires_at > ?
  AND purpose IS NOT NULL
  AND purpose NOT IN ('full', 'totp_enrollment')
	`, formatTime(now)).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return ErrSessionPurposeInvariant
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE identity_sessions SET purpose = 'full'
WHERE revoked_at IS NULL AND expires_at > ? AND purpose IS NULL
	`, formatTime(now)); err != nil {
		return err
	}

	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM identity_sessions
	WHERE revoked_at IS NULL AND expires_at > ?
  AND (purpose IS NULL OR purpose NOT IN ('full', 'totp_enrollment'))
	`, formatTime(now)).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return ErrSessionPurposeInvariant
	}
	return tx.Commit()
}

func (s *Service) PrepareSessionPurposeRollout(ctx context.Context) error {
	return PrepareSessionPurposeRollout(ctx, s.db, s.now())
}
