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
	"omnora/internal/contentref"
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

type Service struct {
	db           *sql.DB
	guard        *access.Guard
	tokens       *aitoken.Service
	now          func() time.Time
	shareURLBase string
}

type Option func(*Service)
type CreateAuditWriter func(context.Context, *sql.Tx, IssuedShare) error
type RevokeAuditWriter func(context.Context, *sql.Tx, string) error

func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}
func WithAITokenService(tokens *aitoken.Service) Option {
	return func(s *Service) {
		if tokens != nil {
			s.tokens = tokens
		}
	}
}
func WithShareURLBase(base string) Option {
	return func(s *Service) {
		if base = strings.TrimSpace(base); base != "" {
			s.shareURLBase = strings.TrimRight(base, "#")
		}
	}
}

func NewService(db *sql.DB, guard *access.Guard, opts ...Option) *Service {
	s := &Service{db: db, guard: guard, now: time.Now, shareURLBase: "/share"}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.tokens == nil && db != nil {
		s.tokens = aitoken.NewService(db)
	}
	return s
}

func (s *Service) List(ctx context.Context, subject access.Subject, filter ListFilter) ([]Share, error) {
	if err := s.validateSubject(ctx, subject, aitoken.ScopeSharesRead); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	mountFilter := strings.TrimSpace(filter.MountID)
	rows, err := s.db.QueryContext(ctx, `
SELECT sh.id,sh.public_id,sh.mount_id,m.display_name,sh.relative_path,
       sh.creator_account_id,COALESCE(a.email,''),COALESCE(a.display_name,''),
       sh.allow_preview,sh.allow_download,sh.max_visits,sh.used_visits,
       sh.max_downloads,sh.used_downloads,sh.expires_at,COALESCE(sh.revoked_at,'')
FROM shares sh
JOIN mounts m ON m.id=sh.mount_id
JOIN accounts a ON a.id=sh.creator_account_id
WHERE sh.creator_account_id=? AND (?='' OR sh.mount_id=?)
ORDER BY sh.created_at DESC,sh.id DESC LIMIT ?
`, subject.AccountID, mountFilter, mountFilter, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		item        Share
		storagePath string
	}
	var candidates []candidate
	now := s.now().UTC()
	for rows.Next() {
		var item Share
		var storagePath, expiresAt, revokedAt string
		var allowPreview, allowDownload int
		var maxVisits, maxDownloads sql.NullInt64
		if err := rows.Scan(&item.ID, &item.PublicID, &item.MountID, &item.MountName, &storagePath,
			&item.CreatorAccountID, &item.CreatorEmail, &item.CreatorDisplayName, &allowPreview, &allowDownload,
			&maxVisits, &item.UsedVisits, &maxDownloads, &item.UsedDownloads, &expiresAt, &revokedAt); err != nil {
			return nil, err
		}
		item.AllowPreview, item.AllowDownload = allowPreview == 1, allowDownload == 1
		if maxVisits.Valid {
			value := maxVisits.Int64
			item.MaxVisits = &value
		}
		if maxDownloads.Valid {
			value := maxDownloads.Int64
			item.MaxDownloads = &value
		}
		item.ExpiresAt, err = parseSQLiteTime(expiresAt)
		if err != nil {
			continue
		}
		if revokedAt != "" {
			if value, parseErr := parseSQLiteTime(revokedAt); parseErr == nil {
				item.RevokedAt = &value
			}
		}
		item.Status = shareStatus(now, item.ExpiresAt, item.RevokedAt)
		candidates = append(candidates, candidate{item: item, storagePath: storagePath})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]Share, 0, len(candidates))
	for _, candidate := range candidates {
		locator, source, visibleMountID, visiblePath, ok := s.locatorForStoredTarget(ctx, candidate.item.CreatorAccountID, candidate.item.MountID, candidate.storagePath)
		if !ok {
			continue
		}
		if _, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: aitoken.ScopeSharesRead, Locator: locator, RequiredPermission: domain.ContentPermissionViewer}); err != nil {
			continue
		}
		candidate.item.Source, candidate.item.MountID, candidate.item.RelativePath = source, visibleMountID, visiblePath
		if source == contentref.SourcePersonal {
			candidate.item.MountName = ""
		}
		items = append(items, candidate.item)
	}
	return items, nil
}

func (s *Service) Create(ctx context.Context, subject access.Subject, req CreateRequest) (IssuedShare, error) {
	return s.CreateSecure(ctx, subject, req, nil)
}

func (s *Service) CreateSecure(ctx context.Context, subject access.Subject, req CreateRequest, auditWriter CreateAuditWriter) (IssuedShare, error) {
	if err := s.validateSubject(ctx, subject, aitoken.ScopeSharesCreate); err != nil {
		return IssuedShare{}, err
	}
	allowPreview, allowDownload := true, true
	if req.AllowPreview != nil {
		allowPreview = *req.AllowPreview
	}
	if req.AllowDownload != nil {
		allowDownload = *req.AllowDownload
	}
	if !allowPreview && !allowDownload {
		return IssuedShare{}, fmt.Errorf("%w: at least one capability is required", ErrInvalidInput)
	}
	if req.MaxVisits != nil && *req.MaxVisits <= 0 {
		return IssuedShare{}, ErrInvalidInput
	}
	if req.MaxDownloads != nil && (*req.MaxDownloads <= 0 || !allowDownload) {
		return IssuedShare{}, ErrInvalidInput
	}
	if !utf8.ValidString(req.Password) || len(req.Password) > maxPasswordBytes || (req.Password != "" && strings.TrimSpace(req.Password) == "") {
		return IssuedShare{}, ErrInvalidInput
	}

	mount, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: aitoken.ScopeSharesCreate, Locator: req.Locator, RequiredPermission: domain.ContentPermissionEditor})
	if err != nil {
		return IssuedShare{}, mapAccessError(err)
	}
	if enabled, err := s.shareEnabled(ctx, mount.ID); err != nil {
		return IssuedShare{}, err
	} else if !enabled {
		return IssuedShare{}, ErrForbidden
	}
	visiblePath, err := files.NewService().ValidateShareTarget(files.Mount{Root: mount.Root, Mode: mount.Mode}, mount.RelativePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
			return IssuedShare{}, fmt.Errorf("%w: target not found", ErrInvalidInput)
		}
		return IssuedShare{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(defaultShareTTL)
	if !req.ExpiresAt.IsZero() {
		expiresAt = req.ExpiresAt.UTC()
	}
	if !now.Before(expiresAt) {
		return IssuedShare{}, ErrInvalidInput
	}
	secret, err := share.NewSecret()
	if err != nil {
		return IssuedShare{}, err
	}
	publicSecret, err := share.NewSecret()
	if err != nil {
		return IssuedShare{}, err
	}
	publicID, shareID := "pub_"+publicSecret, "shr_"+publicSecret
	var passwordHash any
	if req.Password != "" {
		passwordHash, err = share.HashPassword(req.Password)
		if err != nil {
			return IssuedShare{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssuedShare{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
INSERT INTO shares(id,public_id,secret_hash,password_hash,creator_account_id,mount_id,relative_path,
 allow_preview,allow_download,max_visits,max_downloads,expires_at,credential_generation)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,CAST((SELECT value FROM system_state WHERE key='credential_generation') AS INTEGER))
`, shareID, publicID, share.HashSecret(secret), passwordHash, subject.AccountID, mount.ID, mount.StorageRelativePath,
		boolInt(allowPreview), boolInt(allowDownload), req.MaxVisits, req.MaxDownloads, formatSQLiteTime(expiresAt))
	if err != nil {
		return IssuedShare{}, err
	}
	visibleMountID := mount.ID
	if mount.Source == contentref.SourcePersonal {
		visibleMountID = ""
	}
	item := Share{ID: shareID, PublicID: publicID, Source: mount.Source, MountID: visibleMountID, RelativePath: visiblePath,
		CreatorAccountID: subject.AccountID, AllowPreview: allowPreview, AllowDownload: allowDownload,
		MaxVisits: cloneInt64(req.MaxVisits), MaxDownloads: cloneInt64(req.MaxDownloads), ExpiresAt: expiresAt, Status: "active"}
	fragment := publicID + "." + secret
	issued := IssuedShare{Share: item, Secret: secret, Fragment: fragment, URL: s.shareURLBase + "#" + fragment}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, issued); err != nil {
			return IssuedShare{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IssuedShare{}, err
	}
	return issued, nil
}

func (s *Service) Revoke(ctx context.Context, subject access.Subject, shareID string) error {
	return s.RevokeSecure(ctx, subject, shareID, nil)
}
func (s *Service) RevokeSecure(ctx context.Context, subject access.Subject, shareID string, auditWriter RevokeAuditWriter) error {
	item, locator, err := s.loadOwned(ctx, subject, shareID, aitoken.ScopeSharesRevoke)
	if err != nil {
		return err
	}
	if _, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: aitoken.ScopeSharesRevoke, Locator: locator, RequiredPermission: domain.ContentPermissionViewer}); err != nil {
		return mapAccessError(err)
	}
	now := formatSQLiteTime(s.now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE shares SET revoked_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, now, now, item.ID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, item.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) PreviewRevoke(ctx context.Context, subject access.Subject, shareID string) (Share, error) {
	item, locator, err := s.loadOwned(ctx, subject, shareID, aitoken.ScopeSharesRevoke)
	if err != nil {
		return Share{}, err
	}
	if _, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: aitoken.ScopeSharesRevoke, Locator: locator, RequiredPermission: domain.ContentPermissionViewer}); err != nil {
		return Share{}, mapAccessError(err)
	}
	return item, nil
}

func (s *Service) RevokePath(ctx context.Context, mountID, storageRelativePath string) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	return s.revokePath(ctx, s.db, mountID, storageRelativePath)
}

func (s *Service) RevokePathTx(ctx context.Context, tx *sql.Tx, mountID, storageRelativePath string) error {
	if s == nil || tx == nil {
		return ErrUnavailable
	}
	return s.revokePath(ctx, tx, mountID, storageRelativePath)
}

type shareExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Service) revokePath(ctx context.Context, exec shareExecer, mountID, storageRelativePath string) error {
	mountID = strings.TrimSpace(mountID)
	cleaned, err := storage.CleanRelativePath(storageRelativePath)
	if mountID == "" || err != nil {
		return ErrInvalidInput
	}
	now := formatSQLiteTime(s.now().UTC())
	if cleaned == "." {
		_, err = exec.ExecContext(ctx, `UPDATE shares SET revoked_at=?,updated_at=? WHERE mount_id=? AND revoked_at IS NULL`, now, now, mountID)
		return err
	}
	pattern := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(cleaned, `\`, `\\`), `%`, `\%`), `_`, `\_`) + "/%"
	_, err = exec.ExecContext(ctx, `UPDATE shares SET revoked_at=?,updated_at=? WHERE mount_id=? AND revoked_at IS NULL AND (relative_path=? OR relative_path LIKE ? ESCAPE '\')`, now, now, mountID, cleaned, pattern)
	return err
}

func (s *Service) loadOwned(ctx context.Context, subject access.Subject, shareID string, scope aitoken.Scope) (Share, access.Locator, error) {
	if err := s.validateSubject(ctx, subject, scope); err != nil {
		return Share{}, access.Locator{}, err
	}
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return Share{}, access.Locator{}, ErrInvalidInput
	}
	var item Share
	var storedMountID, storagePath, expiresAt, revokedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id,public_id,mount_id,relative_path,creator_account_id,expires_at,COALESCE(revoked_at,'') FROM shares WHERE id=?`, shareID).
		Scan(&item.ID, &item.PublicID, &storedMountID, &storagePath, &item.CreatorAccountID, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) || revokedAt != "" {
		return Share{}, access.Locator{}, ErrNotFound
	}
	if err != nil {
		return Share{}, access.Locator{}, err
	}
	if item.CreatorAccountID != subject.AccountID {
		return Share{}, access.Locator{}, ErrForbidden
	}
	locator, source, mountID, visiblePath, ok := s.locatorForStoredTarget(ctx, item.CreatorAccountID, storedMountID, storagePath)
	if !ok {
		return Share{}, access.Locator{}, ErrUnavailable
	}
	item.Source, item.MountID, item.RelativePath = source, mountID, visiblePath
	item.ExpiresAt, err = parseSQLiteTime(expiresAt)
	if err != nil {
		return Share{}, access.Locator{}, ErrUnavailable
	}
	item.Status = shareStatus(s.now().UTC(), item.ExpiresAt, nil)
	return item, locator, nil
}

func (s *Service) locatorForStoredTarget(ctx context.Context, ownerID, mountID, storagePath string) (access.Locator, contentref.Source, string, string, bool) {
	cleaned, err := storage.CleanRelativePath(storagePath)
	if err != nil {
		return access.Locator{}, "", "", "", false
	}
	var purpose domain.MountPurpose
	var status string
	var shareEnabled int
	if err := s.db.QueryRowContext(ctx, `SELECT purpose,status,share_enabled FROM mounts WHERE id=?`, mountID).Scan(&purpose, &status, &shareEnabled); err != nil || status != "active" || shareEnabled != 1 {
		return access.Locator{}, "", "", "", false
	}
	switch purpose {
	case domain.MountPurposePersonalDefault:
		if cleaned == ownerID {
			cleaned = "."
		} else if strings.HasPrefix(cleaned, ownerID+"/") {
			cleaned = strings.TrimPrefix(cleaned, ownerID+"/")
		} else {
			return access.Locator{}, "", "", "", false
		}
		return access.Locator{Source: contentref.SourcePersonal, Path: cleaned}, contentref.SourcePersonal, "", cleaned, true
	case domain.MountPurposeCommon:
		return access.Locator{Source: contentref.SourceCommonMount, MountID: mountID, Path: cleaned}, contentref.SourceCommonMount, mountID, cleaned, true
	default:
		return access.Locator{}, "", "", "", false
	}
}

func (s *Service) shareEnabled(ctx context.Context, mountID string) (bool, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT share_enabled FROM mounts WHERE id=? AND status='active'`, mountID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrForbidden
	}
	return enabled == 1, err
}

func (s *Service) validateSubject(ctx context.Context, subject access.Subject, scope aitoken.Scope) error {
	if s == nil || s.db == nil || s.guard == nil || strings.TrimSpace(subject.AccountID) == "" {
		return ErrUnauthorized
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id=?`, subject.AccountID).Scan(&status); err != nil || status != "active" {
		return ErrUnauthorized
	}
	if subject.Principal == nil {
		return nil
	}
	if s.tokens == nil || subject.Principal.AccountID != subject.AccountID || strings.TrimSpace(subject.Principal.TokenID) == "" {
		return ErrUnauthorized
	}
	fresh, err := s.tokens.RefreshPrincipal(ctx, subject.Principal.TokenID)
	if err != nil || fresh.AccountID != subject.AccountID {
		return ErrUnauthorized
	}
	if !fresh.HasScope(scope) {
		return ErrForbidden
	}
	return nil
}

func mapAccessError(err error) error {
	switch {
	case errors.Is(err, access.ErrUnauthorized):
		return ErrUnauthorized
	case errors.Is(err, access.ErrForbidden), errors.Is(err, access.ErrReadonlyMount):
		return ErrForbidden
	case errors.Is(err, access.ErrMountUnavailable), errors.Is(err, access.ErrMountIdentityUnverifiable):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	default:
		return err
	}
}

func shareStatus(now, expires time.Time, revoked *time.Time) string {
	if revoked != nil {
		return "revoked"
	}
	if !now.Before(expires) {
		return "expired"
	}
	return "active"
}
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
	cloned := *value
	return &cloned
}
func formatSQLiteTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func parseSQLiteTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q", value)
}
