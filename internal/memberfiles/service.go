package memberfiles

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/catalog"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/storage"
)

var (
	ErrInvalidInput = errors.New("member files: invalid input")
	ErrUnauthorized = errors.New("member files: unauthorized")
)

// Service composes live access checks with low-level file and catalog
// services. It never accepts or returns a host filesystem path.
type Service struct {
	db      *sql.DB
	guard   *access.Guard
	tokens  *aitoken.Service
	catalog catalog.Service
	files   files.Service
	now     func() time.Time
	shares  ShareInvalidator
}

// NewService constructs a shared member file service. The catalog service is
// passed in explicitly so REST and MCP adapters use the same implementation.
func NewService(db *sql.DB, guard *access.Guard, catalog catalog.Service, opts ...Option) *Service {
	s := &Service{
		db:      db,
		guard:   guard,
		catalog: catalog,
		files:   files.NewService(),
		now:     time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
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

// ListSpaces returns active spaces that are visible to the account and, for a
// token subject, represented by at least one current token boundary. Mount
// roots are intentionally not part of the result.
func (s *Service) ListSpaces(ctx context.Context, subject access.Subject) ([]Space, error) {
	if err := s.validate(subject); err != nil {
		return nil, err
	}
	// Unlike mount listing, a space can be visible before it has an active
	// mount. Refresh token subjects here because this method has no locator on
	// which AccessGuard could otherwise perform the token check.
	if subject.Principal != nil {
		tokens := s.tokens
		if tokens == nil {
			tokens = aitoken.NewService(s.db)
		}
		fresh, err := tokens.RefreshPrincipal(ctx, subject.Principal.TokenID)
		if err != nil || fresh.AccountID != subject.AccountID || !fresh.HasScope(aitoken.ScopeSpacesRead) {
			if err != nil {
				return nil, err
			}
			return nil, access.ErrForbidden
		}
		subject.Principal = &fresh
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sp.id, sp.kind, sp.name
FROM spaces sp
JOIN space_members sm ON sm.space_id = sp.id AND sm.account_id = ?
JOIN accounts a ON a.id = sm.account_id AND a.status = 'active'
WHERE sp.status = 'active'
ORDER BY sp.kind, sp.name, sp.id
`, subject.AccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	spaces := make([]Space, 0)
	for rows.Next() {
		var space Space
		if err := rows.Scan(&space.ID, &space.Kind, &space.Name); err != nil {
			return nil, err
		}
		spaces = append(spaces, space)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	visible := spaces[:0]
	for _, space := range spaces {
		// AccessGuard remains the source of truth for live ACL membership.
		if !s.guard.HasSpacePermission(ctx, subject.AccountID, space.ID, domain.SpacePermissionViewer) {
			continue
		}
		if subject.Principal != nil {
			// A persisted boundary alone is not sufficient: the mount may have
			// been disabled, become unavailable, drifted in identity, or no
			// longer belong to this space. visibleMounts replays the complete
			// live Guard path for every current boundary.
			records, err := s.visibleMounts(ctx, subject, space.ID, aitoken.ScopeSpacesRead)
			if err != nil {
				return nil, err
			}
			if len(records) == 0 {
				continue
			}
		}
		visible = append(visible, space)
	}
	return visible, nil
}

// ListMounts returns active mounts visible in one active space. The result
// contains only member-safe metadata and never exposes Root.
func (s *Service) ListMounts(ctx context.Context, subject access.Subject, spaceID string) ([]Mount, error) {
	if err := s.validate(subject); err != nil {
		return nil, err
	}
	subject, err := s.requireTokenScope(ctx, subject, aitoken.ScopeSpacesRead)
	if err != nil {
		return nil, err
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return nil, ErrInvalidInput
	}
	records, err := s.visibleMounts(ctx, subject, spaceID, aitoken.ScopeSpacesRead)
	if err != nil {
		return nil, err
	}
	mounts := make([]Mount, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, ok := seen[record.mount.ID]; ok {
			continue
		}
		seen[record.mount.ID] = struct{}{}
		mounts = append(mounts, record.mount)
	}
	return mounts, nil
}

// validate checks only request shape. Authorization is deliberately delegated
// to AccessGuard for every operation and is never cached on Service.
func (s *Service) validate(subject access.Subject) error {
	if s == nil || s.db == nil || s.guard == nil || strings.TrimSpace(subject.AccountID) == "" {
		return ErrUnauthorized
	}
	return nil
}

// requireTokenScope gives collection operations an explicit scope failure
// even when no mount remains visible. AccessGuard repeats this check for each
// actual locator, so this helper is only an early, stable error boundary.
func (s *Service) requireTokenScope(ctx context.Context, subject access.Subject, scope aitoken.Scope) (access.Subject, error) {
	if subject.Principal == nil {
		return subject, nil
	}
	tokens := s.tokens
	if tokens == nil {
		tokens = aitoken.NewService(s.db)
	}
	fresh, err := tokens.RefreshPrincipal(ctx, subject.Principal.TokenID)
	if err != nil {
		return subject, err
	}
	if fresh.AccountID != subject.AccountID {
		return subject, access.ErrUnauthorized
	}
	if !fresh.HasScope(scope) {
		return subject, access.ErrForbidden
	}
	subject.Principal = &fresh
	return subject, nil
}

type mountRecord struct {
	space      Space
	mount      Mount
	root       string
	identity   string
	authorized access.AuthorizedMount
	boundaries []string
}

// visibleMounts loads only active ACL rows, then asks AccessGuard to
// authorize each current token boundary. This makes list/search results
// fail closed when ACLs, token scopes, boundaries, mount state, or identity
// drift after a token was issued.
func (s *Service) visibleMounts(ctx context.Context, subject access.Subject, spaceID string, scope aitoken.Scope) ([]mountRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT sp.id, sp.kind, sp.name,
       m.id, m.space_id, m.display_name, m.kind, m.mode,
       m.root_path, COALESCE(m.mount_identity_json, '')
FROM spaces sp
JOIN space_members sm ON sm.space_id = sp.id AND sm.account_id = ?
JOIN mounts m ON m.space_id = sp.id
WHERE sp.status = 'active'
  AND m.status = 'active'
  AND (? = '' OR sp.id = ?)
ORDER BY sp.name, sp.id, m.display_name, m.id
`, subject.AccountID, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []mountRecord
	for rows.Next() {
		var record mountRecord
		if err := rows.Scan(
			&record.space.ID, &record.space.Kind, &record.space.Name,
			&record.mount.ID, &record.mount.SpaceID, &record.mount.Name,
			&record.mount.Kind, &record.mount.Mode, &record.root, &record.identity,
		); err != nil {
			return nil, err
		}
		record.mount.ReadOnly = record.mount.Mode == domain.MountModeReadOnly
		candidates = append(candidates, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	var records []mountRecord
	for _, record := range candidates {
		boundaries, err := s.currentBoundaries(ctx, subject, record.space.ID, record.mount.ID)
		if err != nil {
			return nil, err
		}
		for _, boundary := range boundaries {
			path := boundary
			if path == "" {
				path = "."
			}
			authorized, authErr := s.guard.Authorize(ctx, access.CheckRequest{
				Subject:            subject,
				Scope:              scope,
				Locator:            access.Locator{SpaceID: record.space.ID, MountID: record.mount.ID, Path: path},
				RequiredPermission: domain.SpacePermissionViewer,
			})
			if authErr != nil {
				continue
			}
			record.authorized = authorized
			record.boundaries = append(record.boundaries, authorized.RelativePath)
		}
		if len(record.boundaries) > 0 {
			records = append(records, record)
		}
	}
	return records, nil
}

// currentBoundaries is a live database read. Browser sessions can see the
// whole mount; token subjects must use the currently persisted boundaries,
// not the potentially stale copy that authenticated the request earlier.
func (s *Service) currentBoundaries(ctx context.Context, subject access.Subject, spaceID, mountID string) ([]string, error) {
	if subject.Principal == nil {
		return []string{"."}, nil
	}
	if strings.TrimSpace(subject.Principal.TokenID) == "" {
		return nil, ErrUnauthorized
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT relative_path
	FROM ai_token_boundaries
WHERE token_id = ? AND space_id = ? AND mount_id = ?
ORDER BY relative_path
`, subject.Principal.TokenID, spaceID, mountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boundaries []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		cleaned, err := storage.CleanRelativePath(value)
		if err != nil {
			continue
		}
		boundaries = append(boundaries, cleaned)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return boundaries, nil
}
