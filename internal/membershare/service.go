package membershare

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/share"
	"omnora/internal/storage"
)

const (
	defaultShareTTL  = 7 * 24 * time.Hour
	defaultListLimit = 100
	maxListLimit     = 500
	maxPasswordBytes = 1024
)

var (
	ErrInvalidInput = errors.New("member share: invalid input")
	ErrUnauthorized = errors.New("member share: unauthorized")
	ErrForbidden    = errors.New("member share: forbidden")
	ErrNotFound     = errors.New("member share: not found")
	ErrUnavailable  = errors.New("member share: unavailable")
)

// Service owns the authenticated member share lifecycle. It deliberately
// does not implement visitor exchange or share-session authorization; those
// remain in internal/share.
type Service struct {
	db           *sql.DB
	guard        *access.Guard
	tokens       *aitoken.Service
	now          func() time.Time
	shareURLBase string
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

// WithAITokenService injects the process-wide token validator used for live
// scope/boundary refreshes. Tests and standalone callers may omit it; the
// service then falls back to a local validator for compatibility.
func WithAITokenService(tokens *aitoken.Service) Option {
	return func(s *Service) {
		if tokens != nil {
			s.tokens = tokens
		}
	}
}

// WithShareURLBase configures the public share portal base used when a newly
// issued fragment is rendered as a URL. The default is the relative portal
// path /share, which is safe for deployments behind any host name.
func WithShareURLBase(base string) Option {
	return func(s *Service) {
		base = strings.TrimSpace(base)
		if base != "" {
			s.shareURLBase = strings.TrimRight(base, "#")
		}
	}
}

func NewService(db *sql.DB, guard *access.Guard, opts ...Option) *Service {
	s := &Service{
		db:           db,
		guard:        guard,
		now:          time.Now,
		shareURLBase: "/share",
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// List returns shares created by the caller or belonging to a space where the
// caller is currently a manager. It never selects the persisted fragment
// secret or password hash.
func (s *Service) List(ctx context.Context, subject access.Subject, filter ListFilter) ([]Share, error) {
	if err := s.validateSubject(ctx, subject, aitoken.ScopeSharesRead); err != nil {
		return nil, err
	}
	spaceID := strings.TrimSpace(filter.SpaceID)
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sh.id, sh.public_id, sh.space_id, COALESCE(sp.name, ''),
       sh.mount_id, COALESCE(m.display_name, ''), sh.relative_path,
       sh.creator_account_id, COALESCE(a.email, ''), COALESCE(a.display_name, ''),
       sh.allow_preview, sh.allow_download, sh.max_visits, sh.used_visits,
       sh.max_downloads, sh.used_downloads, sh.expires_at, COALESCE(sh.revoked_at, '')
FROM shares sh
JOIN spaces sp ON sp.id = sh.space_id
JOIN mounts m ON m.id = sh.mount_id AND m.space_id = sh.space_id
JOIN accounts a ON a.id = sh.creator_account_id
WHERE sp.status = 'active' AND m.status = 'active'
  AND (sh.creator_account_id = ? OR EXISTS (
          SELECT 1 FROM space_members manager
          WHERE manager.space_id = sh.space_id
            AND manager.account_id = ?
            AND manager.permission = 'manager'
      ))
  AND (? = '' OR sh.space_id = ?)
ORDER BY sh.created_at DESC, sh.id DESC
LIMIT ?
`, subject.AccountID, subject.AccountID, spaceID, spaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Buffer rows before per-item live checks. SQLite may expose only one
	// connection in constrained deployments; querying Guard while a rows
	// cursor is open can otherwise deadlock that connection.
	candidates := make([]Share, 0)
	now := s.now().UTC()
	for rows.Next() {
		item, err := scanShare(rows, now)
		if err != nil {
			return nil, err
		}
		if _, err := cleanSharePath(item.RelativePath); err != nil {
			// A legacy or manually-corrupted row must never become a path
			// disclosure or a boundary bypass in a member listing.
			continue
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	items := make([]Share, 0, len(candidates))
	for _, item := range candidates {
		// A creator retains visibility of their own share even if they are no
		// longer an ACL member. A current manager, or any non-creator, must
		// pass the normal live viewer check.
		if item.CreatorAccountID == subject.AccountID {
			mount, err := s.guard.LoadMount(ctx, item.SpaceID, item.MountID)
			if err != nil || s.guard.VerifyMountIdentity(ctx, mount) != nil {
				continue
			}
		} else if _, err := s.guard.Authorize(ctx, access.CheckRequest{
			Subject:            subject,
			Scope:              aitoken.ScopeSharesRead,
			Locator:            access.Locator{SpaceID: item.SpaceID, MountID: item.MountID, Path: item.RelativePath},
			RequiredPermission: domain.SpacePermissionViewer,
		}); err != nil {
			continue
		}
		if subject.Principal != nil && !withinBoundaries(*subject.Principal, item.SpaceID, item.MountID, item.RelativePath) {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// Create validates a manager-authorized target, stores only a hash of the
// fragment secret, and returns the complete capability exactly once.
func (s *Service) Create(ctx context.Context, subject access.Subject, req CreateRequest) (IssuedShare, error) {
	if err := s.validateSubject(ctx, subject, aitoken.ScopeSharesCreate); err != nil {
		return IssuedShare{}, err
	}
	if strings.TrimSpace(req.Locator.SpaceID) == "" || strings.TrimSpace(req.Locator.MountID) == "" {
		return IssuedShare{}, ErrInvalidInput
	}
	allowPreview := true
	if req.AllowPreview != nil {
		allowPreview = *req.AllowPreview
	}
	allowDownload := true
	if req.AllowDownload != nil {
		allowDownload = *req.AllowDownload
	}
	if !allowPreview && !allowDownload {
		return IssuedShare{}, fmt.Errorf("%w: at least one share capability is required", ErrInvalidInput)
	}
	if req.MaxVisits != nil && *req.MaxVisits <= 0 {
		return IssuedShare{}, fmt.Errorf("%w: max visits must be positive", ErrInvalidInput)
	}
	if req.MaxDownloads != nil {
		if *req.MaxDownloads <= 0 || !allowDownload {
			return IssuedShare{}, fmt.Errorf("%w: max downloads must be positive and download must be enabled", ErrInvalidInput)
		}
	}
	if !utf8.ValidString(req.Password) || len(req.Password) > maxPasswordBytes || (req.Password != "" && strings.TrimSpace(req.Password) == "") {
		return IssuedShare{}, fmt.Errorf("%w: password is invalid", ErrInvalidInput)
	}

	mount, err := s.guard.Authorize(ctx, access.CheckRequest{
		Subject:            subject,
		Scope:              aitoken.ScopeSharesCreate,
		Locator:            access.Locator{SpaceID: req.Locator.SpaceID, MountID: req.Locator.MountID, Path: req.Locator.Path},
		RequiredPermission: domain.SpacePermissionManager,
	})
	if err != nil {
		return IssuedShare{}, mapAccessError(err)
	}
	relativePath, err := files.NewService().ValidateShareTarget(files.Mount{Root: mount.Root, Mode: mount.Mode, Kind: mount.Kind}, mount.RelativePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
			return IssuedShare{}, fmt.Errorf("%w: share target was not found", ErrInvalidInput)
		}
		return IssuedShare{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	now := s.now().UTC()
	expiresAt := now.Add(defaultShareTTL)
	if !req.ExpiresAt.IsZero() {
		expiresAt = req.ExpiresAt.UTC()
	}
	if !now.Before(expiresAt) {
		return IssuedShare{}, fmt.Errorf("%w: expiry must be in the future", ErrInvalidInput)
	}
	secret, err := share.NewSecret()
	if err != nil {
		return IssuedShare{}, err
	}
	publicID, err := share.NewSecret()
	if err != nil {
		return IssuedShare{}, err
	}
	publicID = "pub_" + publicID
	shareID := "shr_" + publicID[4:]
	var passwordHash any
	if req.Password != "" {
		passwordHash, err = share.HashPassword(req.Password)
		if err != nil {
			return IssuedShare{}, err
		}
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO shares(
    id, public_id, secret_hash, password_hash, creator_account_id,
    space_id, mount_id, relative_path, allow_preview, allow_download,
    max_visits, max_downloads, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, shareID, publicID, share.HashSecret(secret), passwordHash, subject.AccountID,
		req.Locator.SpaceID, req.Locator.MountID, relativePath, boolInt(allowPreview), boolInt(allowDownload),
		req.MaxVisits, req.MaxDownloads, formatSQLiteTime(expiresAt))
	if err != nil {
		return IssuedShare{}, err
	}
	fragment := publicID + "." + secret
	item := Share{
		ID: shareID, PublicID: publicID,
		SpaceID: req.Locator.SpaceID, MountID: req.Locator.MountID, RelativePath: relativePath,
		CreatorAccountID: subject.AccountID, AllowPreview: allowPreview, AllowDownload: allowDownload,
		MaxVisits: cloneInt64(req.MaxVisits), MaxDownloads: cloneInt64(req.MaxDownloads),
		ExpiresAt: expiresAt, Status: "active",
	}
	return IssuedShare{Share: item, Secret: secret, Fragment: fragment, URL: s.shareURLBase + "#" + fragment}, nil
}

// Revoke revokes a share for its creator or a current manager of its space.
// It validates the current mount identity and token boundary before changing
// public reachability.
func (s *Service) Revoke(ctx context.Context, subject access.Subject, shareID string) error {
	if err := s.validateSubject(ctx, subject, aitoken.ScopeSharesRevoke); err != nil {
		return err
	}
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return ErrInvalidInput
	}
	var spaceID, mountID, relativePath, creatorID string
	var revokedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT space_id, mount_id, relative_path, creator_account_id, revoked_at
FROM shares WHERE id = ?
`, shareID).Scan(&spaceID, &mountID, &relativePath, &creatorID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if revokedAt.Valid {
		return ErrNotFound
	}
	if _, err := cleanSharePath(relativePath); err != nil {
		return ErrUnavailable
	}
	manager := s.guard.HasSpacePermission(ctx, subject.AccountID, spaceID, domain.SpacePermissionManager)
	if creatorID != subject.AccountID && !manager {
		return ErrForbidden
	}
	if subject.Principal != nil && !withinBoundaries(*subject.Principal, spaceID, mountID, relativePath) {
		return access.ErrBoundaryViolation
	}
	mount, err := s.guard.LoadMount(ctx, spaceID, mountID)
	if err != nil {
		return mapAccessError(err)
	}
	if err := s.guard.VerifyMountIdentity(ctx, mount); err != nil {
		return mapAccessError(err)
	}
	now := formatSQLiteTime(s.now().UTC())
	result, err := s.db.ExecContext(ctx, `
UPDATE shares SET revoked_at = ?, updated_at = ?
WHERE id = ? AND revoked_at IS NULL
`, now, now, shareID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

// PreviewRevoke performs the same live scope, creator/manager, boundary and
// mount-identity checks as Revoke without changing share state.
func (s *Service) PreviewRevoke(ctx context.Context, subject access.Subject, shareID string) (Share, error) {
	if err := s.validateSubject(ctx, subject, aitoken.ScopeSharesRevoke); err != nil {
		return Share{}, err
	}
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return Share{}, ErrInvalidInput
	}
	var item Share
	var expiresAt string
	var revokedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, public_id, space_id, mount_id, relative_path, creator_account_id, expires_at, COALESCE(revoked_at, '') FROM shares WHERE id = ?`, shareID).Scan(&item.ID, &item.PublicID, &item.SpaceID, &item.MountID, &item.RelativePath, &item.CreatorAccountID, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	item.ExpiresAt, err = parseSQLiteTime(expiresAt)
	if err != nil {
		return Share{}, ErrUnavailable
	}
	if revokedAt.Valid {
		return Share{}, ErrNotFound
	}
	if _, err := cleanSharePath(item.RelativePath); err != nil {
		return Share{}, ErrUnavailable
	}
	manager := s.guard.HasSpacePermission(ctx, subject.AccountID, item.SpaceID, domain.SpacePermissionManager)
	if item.CreatorAccountID != subject.AccountID && !manager {
		return Share{}, ErrForbidden
	}
	if subject.Principal != nil && !withinBoundaries(*subject.Principal, item.SpaceID, item.MountID, item.RelativePath) {
		return Share{}, access.ErrBoundaryViolation
	}
	mount, err := s.guard.LoadMount(ctx, item.SpaceID, item.MountID)
	if err != nil {
		return Share{}, mapAccessError(err)
	}
	if err := s.guard.VerifyMountIdentity(ctx, mount); err != nil {
		return Share{}, mapAccessError(err)
	}
	return item, nil
}

// RevokePath invalidates shares at a path and descendants after a successful
// member-file mutation. It intentionally requires no caller authorization:
// the mutation service invokes it only after its own guarded operation.
func (s *Service) RevokePath(ctx context.Context, spaceID, mountID, relativePath string) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	spaceID = strings.TrimSpace(spaceID)
	mountID = strings.TrimSpace(mountID)
	if spaceID == "" || mountID == "" {
		return ErrInvalidInput
	}
	cleaned, err := cleanSharePath(relativePath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	now := formatSQLiteTime(s.now().UTC())
	condition := `relative_path = ?`
	args := []any{cleaned}
	if cleaned == "." {
		condition = `1 = 1`
	} else {
		condition = `relative_path = ? OR relative_path LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(cleaned)+"/%")
	}
	args = append([]any{now, now, spaceID, mountID}, args...)
	_, err = s.db.ExecContext(ctx, `
UPDATE shares SET revoked_at = ?, updated_at = ?
WHERE space_id = ? AND mount_id = ? AND revoked_at IS NULL AND `+condition+`
`, args...)
	return err
}

func (s *Service) validateSubject(ctx context.Context, subject access.Subject, scope aitoken.Scope) error {
	if s == nil || s.db == nil || s.guard == nil || strings.TrimSpace(subject.AccountID) == "" {
		return ErrUnauthorized
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id = ?`, subject.AccountID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		return err
	}
	if status != "active" {
		return ErrUnauthorized
	}
	if subject.Principal == nil {
		return nil
	}
	if subject.Principal.AccountID != subject.AccountID || strings.TrimSpace(subject.Principal.TokenID) == "" {
		return ErrUnauthorized
	}
	tokens := s.tokens
	if tokens == nil {
		tokens = aitoken.NewService(s.db)
	}
	fresh, err := tokens.RefreshPrincipal(ctx, subject.Principal.TokenID)
	if err != nil {
		return ErrUnauthorized
	}
	if fresh.AccountID != subject.AccountID {
		return ErrUnauthorized
	}
	if !fresh.HasScope(scope) {
		return ErrForbidden
	}
	// Keep the caller's principal current for boundary checks in this request.
	*subject.Principal = fresh
	return nil
}

func mapAccessError(err error) error {
	switch {
	case errors.Is(err, access.ErrUnauthorized):
		return ErrUnauthorized
	case errors.Is(err, access.ErrForbidden):
		return ErrForbidden
	default:
		return err
	}
}

func withinBoundaries(principal aitoken.Principal, spaceID, mountID, relativePath string) bool {
	cleaned, err := cleanSharePath(relativePath)
	if err != nil {
		return false
	}
	for _, boundary := range principal.Boundaries {
		if boundary.SpaceID != spaceID || boundary.MountID != mountID {
			continue
		}
		boundaryPath, err := storage.CleanRelativePath(boundary.RelativePath)
		if err != nil {
			continue
		}
		if boundaryPath == "." || cleaned == boundaryPath || strings.HasPrefix(cleaned, boundaryPath+"/") {
			return true
		}
	}
	return false
}

// cleanSharePath is stricter than path.Clean: a persisted share path is
// expected to have been normalized at creation time, so a legacy row that
// contains an explicit traversal component is rejected rather than silently
// rewritten to a different object.
func cleanSharePath(value string) (string, error) {
	if strings.Contains(value, `\`) {
		return "", errors.New("backslash paths are not allowed")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", errors.New("path traversal is not allowed")
		}
	}
	cleaned, err := storage.CleanRelativePath(value)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) != "" && cleaned != value {
		return "", errors.New("path must be normalized")
	}
	return cleaned, nil
}

func scanShare(rows *sql.Rows, now time.Time) (Share, error) {
	var item Share
	var allowPreview, allowDownload int
	var maxVisits, maxDownloads sql.NullInt64
	var expiresAt, revokedAt string
	if err := rows.Scan(
		&item.ID, &item.PublicID, &item.SpaceID, &item.SpaceName,
		&item.MountID, &item.MountName, &item.RelativePath,
		&item.CreatorAccountID, &item.CreatorEmail, &item.CreatorDisplayName,
		&allowPreview, &allowDownload, &maxVisits, &item.UsedVisits,
		&maxDownloads, &item.UsedDownloads, &expiresAt, &revokedAt,
	); err != nil {
		return Share{}, err
	}
	var err error
	item.ExpiresAt, err = parseSQLiteTime(expiresAt)
	if err != nil {
		return Share{}, err
	}
	if revokedAt != "" {
		parsedRevokedAt, parseErr := parseSQLiteTime(revokedAt)
		err = parseErr
		if err != nil {
			return Share{}, err
		}
		item.RevokedAt = &parsedRevokedAt
	}
	item.AllowPreview = allowPreview == 1
	item.AllowDownload = allowDownload == 1
	if maxVisits.Valid {
		item.MaxVisits = cloneInt64(&maxVisits.Int64)
	}
	if maxDownloads.Valid {
		item.MaxDownloads = cloneInt64(&maxDownloads.Int64)
	}
	switch {
	case item.RevokedAt != nil:
		item.Status = "revoked"
	case !now.Before(item.ExpiresAt):
		item.Status = "expired"
	case (item.MaxVisits != nil && item.UsedVisits >= *item.MaxVisits) || (item.MaxDownloads != nil && item.UsedDownloads >= *item.MaxDownloads):
		item.Status = "exhausted"
	default:
		item.Status = "active"
	}
	return item, nil
}

func parseSQLiteTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid share timestamp")
}

func formatSQLiteTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}
