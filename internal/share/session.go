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
	SpaceID       string
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
	err := s.db.QueryRowContext(ctx, `
SELECT ss.share_id, ss.id, ss.generation, ss.expires_at, ss.revoked_at,
       sh.space_id, sh.mount_id, sh.relative_path, sh.allow_preview, sh.allow_download,
       sh.max_downloads, sh.used_downloads, sh.expires_at, sh.revoked_at, sh.generation
FROM share_sessions ss
JOIN shares sh ON sh.id = ss.share_id
WHERE ss.session_hash = ?
`, hash).Scan(
		&principal.ShareID,
		&principal.SessionID,
		&sessionGeneration,
		&sessionExpiresAt,
		&sessionRevokedAt,
		&principal.SpaceID,
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
