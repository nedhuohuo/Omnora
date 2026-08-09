package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SessionPrincipal describes an authenticated share portal visitor: the share
// they were granted access to and the boundary they may operate within.
type SessionPrincipal struct {
	ShareID       string
	SessionID     string
	MountID       string
	RelativePath  string
	AllowPreview  bool
	AllowDownload bool
	MaxDownloads  sql.NullInt64
	UsedDownloads int
	ShareExpires  time.Time
	Generation    int
}

// VerifySession resolves a share session cookie value into the share it
// grants access to. It fails closed on any expiry, revocation, or
// generation mismatch so that revoking or regenerating a share immediately
// invalidates every previously issued share session.
func (s *Service) VerifySession(ctx context.Context, token string) (SessionPrincipal, error) {
	if s == nil || s.db == nil || token == "" {
		return SessionPrincipal{}, &ExchangeError{Code: CodeShareUnavailable}
	}
	now := s.now().UTC()
	hash := HashSecret(token)

	var principal SessionPrincipal
	var sessionExpiresAt, shareExpiresAt string
	var sessionRevokedAt, shareRevokedAt sql.NullString
	var sessionGeneration, shareGeneration int
	var allowPreview, allowDownload int
	// Requiring the session's credential_generation to still match the
	// current system epoch means bumping it (password reset, security
	// event, ...) immediately invalidates every previously issued share
	// session, the same way it already invalidates identity sessions.
	err := s.db.QueryRowContext(ctx, `
SELECT ss.share_id, ss.id, ss.generation, ss.expires_at, ss.revoked_at,
       sh.mount_id, sh.relative_path, sh.allow_preview, sh.allow_download,
       sh.max_downloads, sh.used_downloads, sh.expires_at, sh.revoked_at, sh.generation
FROM share_sessions ss
JOIN shares sh ON sh.id = ss.share_id
JOIN accounts creator ON creator.id = sh.creator_account_id AND creator.status = 'active'
JOIN mounts m ON m.id = sh.mount_id AND m.status = 'active' AND m.share_enabled = 1
WHERE ss.session_hash = ?
  AND (
    (m.purpose = 'personal_default'
      AND (sh.relative_path = sh.creator_account_id OR sh.relative_path LIKE sh.creator_account_id || '/%')
      AND EXISTS (SELECT 1 FROM personal_directories pd WHERE pd.account_id = sh.creator_account_id AND pd.state = 'ready'))
    OR
    (m.purpose = 'common' AND EXISTS (
      SELECT 1 FROM mount_grants mg
      WHERE mg.mount_id = sh.mount_id AND mg.account_id = sh.creator_account_id AND mg.permission = 'editor'
    ))
  )
  AND ss.credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
`, hash).Scan(
		&principal.ShareID,
		&principal.SessionID,
		&sessionGeneration,
		&sessionExpiresAt,
		&sessionRevokedAt,
		&principal.MountID,
		&principal.RelativePath,
		&allowPreview,
		&allowDownload,
		&principal.MaxDownloads,
		&principal.UsedDownloads,
		&shareExpiresAt,
		&shareRevokedAt,
		&shareGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionPrincipal{}, &ExchangeError{Code: CodeShareUnavailable}
	}
	if err != nil {
		return SessionPrincipal{}, exchangeDBError(err)
	}

	sessionExpires, err := parseSQLiteTime(sessionExpiresAt)
	if err != nil {
		return SessionPrincipal{}, exchangeDBError(fmt.Errorf("parse share session expiry: %w", err))
	}
	shareExpires, err := parseSQLiteTime(shareExpiresAt)
	if err != nil {
		return SessionPrincipal{}, exchangeDBError(fmt.Errorf("parse share expiry: %w", err))
	}
	if sessionRevokedAt.Valid || shareRevokedAt.Valid {
		return SessionPrincipal{}, &ExchangeError{Code: CodeShareUnavailable}
	}
	if !now.Before(sessionExpires) || !now.Before(shareExpires) {
		return SessionPrincipal{}, &ExchangeError{Code: CodeShareUnavailable}
	}
	if sessionGeneration != shareGeneration {
		return SessionPrincipal{}, &ExchangeError{Code: CodeShareUnavailable}
	}

	principal.AllowPreview = allowPreview == 1
	principal.AllowDownload = allowDownload == 1
	principal.ShareExpires = shareExpires
	principal.Generation = shareGeneration
	return principal, nil
}

// IncrementDownload atomically increments used_downloads for a share,
// enforcing max_downloads (when set) the same way Exchange enforces
// max_visits. It fails closed if the share was revoked, expired, or
// regenerated since the session was issued.
func (s *Service) IncrementDownload(ctx context.Context, shareID string, generation int) error {
	if s == nil || s.db == nil {
		return &ExchangeError{Code: CodeShareUnavailable}
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE shares
SET used_downloads = used_downloads + 1,
    updated_at = ?
WHERE id = ?
  AND revoked_at IS NULL
  AND expires_at > ?
  AND generation = ?
  AND credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
  AND EXISTS (
    SELECT 1 FROM accounts creator
    JOIN mounts m ON m.id = shares.mount_id
    WHERE creator.id = shares.creator_account_id
      AND creator.status = 'active'
      AND m.status = 'active'
      AND m.share_enabled = 1
      AND (
        (m.purpose = 'personal_default'
          AND (shares.relative_path = shares.creator_account_id OR shares.relative_path LIKE shares.creator_account_id || '/%')
          AND EXISTS (SELECT 1 FROM personal_directories pd WHERE pd.account_id = shares.creator_account_id AND pd.state = 'ready'))
        OR
        (m.purpose = 'common' AND EXISTS (
          SELECT 1 FROM mount_grants mg
          WHERE mg.mount_id = shares.mount_id AND mg.account_id = shares.creator_account_id AND mg.permission = 'editor'
        ))
      )
  )
  AND (max_downloads IS NULL OR used_downloads < max_downloads)
`, formatSQLiteTime(now), shareID, formatSQLiteTime(now), generation)
	if err != nil {
		return exchangeDBError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return exchangeDBError(err)
	}
	if affected != 1 {
		return &ExchangeError{Code: CodeShareUnavailable}
	}
	return nil
}
