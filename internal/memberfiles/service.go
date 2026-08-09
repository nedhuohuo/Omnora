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
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/fileops"
	"omnora/internal/files"
)

var (
	ErrInvalidInput = errors.New("member files: invalid input")
	ErrUnauthorized = errors.New("member files: unauthorized")
)

type Service struct {
	db      *sql.DB
	guard   *access.Guard
	tokens  *aitoken.Service
	catalog catalog.Service
	files   files.Service
	now     func() time.Time
	shares  ShareInvalidator
	fileOps *fileops.Coordinator
}

func NewService(db *sql.DB, guard *access.Guard, catalog catalog.Service, opts ...Option) *Service {
	s := &Service{db: db, guard: guard, catalog: catalog, files: files.NewService(), now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func WithFileOpsCoordinator(coordinator *fileops.Coordinator) Option {
	return func(s *Service) { s.fileOps = coordinator }
}

func WithAITokenService(tokens *aitoken.Service) Option {
	return func(s *Service) {
		if tokens != nil {
			s.tokens = tokens
		}
	}
}

// ListMounts returns only currently granted common mounts. The protected
// personal-default mount is addressed as source=personal and is never listed.
func (s *Service) ListMounts(ctx context.Context, subject access.Subject) ([]Mount, error) {
	if err := s.validate(subject); err != nil {
		return nil, err
	}
	if err := s.requireActiveCollectionAccount(ctx, subject.AccountID); err != nil {
		return nil, err
	}
	subject, err := s.requireTokenScope(ctx, subject, aitoken.ScopeMountsRead)
	if err != nil {
		return nil, err
	}
	records, err := s.visibleMounts(ctx, subject, aitoken.ScopeMountsRead, contentref.SourceCommonMount, "")
	if err != nil {
		return nil, err
	}
	mounts := make([]Mount, 0, len(records))
	for _, record := range records {
		mounts = append(mounts, record.mount)
	}
	return mounts, nil
}

func (s *Service) validate(subject access.Subject) error {
	if s == nil || s.db == nil || s.guard == nil || strings.TrimSpace(subject.AccountID) == "" {
		return ErrUnauthorized
	}
	return nil
}

func (s *Service) requireActiveCollectionAccount(ctx context.Context, accountID string) error {
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM accounts WHERE id = ? AND status = 'active'`, accountID).Scan(&active); err != nil {
		return err
	}
	if active != 1 {
		return ErrUnauthorized
	}
	return nil
}

func (s *Service) requireTokenScope(ctx context.Context, subject access.Subject, scope aitoken.Scope) (access.Subject, error) {
	if subject.Principal == nil {
		return subject, nil
	}
	if strings.TrimSpace(subject.Principal.TokenID) == "" || subject.Principal.AccountID != subject.AccountID {
		return subject, access.ErrUnauthorized
	}
	tokens := s.tokens
	if tokens == nil {
		tokens = aitoken.NewService(s.db)
	}
	fresh, err := tokens.RefreshPrincipal(ctx, subject.Principal.TokenID)
	if err != nil || fresh.AccountID != subject.AccountID {
		return subject, access.ErrUnauthorized
	}
	if !fresh.HasScope(scope) {
		return subject, access.ErrForbidden
	}
	subject.Principal = &fresh
	return subject, nil
}

type mountRecord struct {
	source     contentref.Source
	mount      Mount
	authorized access.AuthorizedMount
	boundaries []string
}

// visibleMounts replays Guard authorization for every live token boundary.
// source may be personal, common_mount, or all_account_content.
func (s *Service) visibleMounts(ctx context.Context, subject access.Subject, scope aitoken.Scope, source contentref.Source, mountID string) ([]mountRecord, error) {
	if source != contentref.SourcePersonal && source != contentref.SourceCommonMount && source != aitoken.SourceAllAccountContent {
		return nil, ErrInvalidInput
	}
	var candidates []mountRecord
	if source == contentref.SourcePersonal || source == aitoken.SourceAllAccountContent {
		candidates = append(candidates, mountRecord{source: contentref.SourcePersonal})
	}
	if source == contentref.SourceCommonMount || source == aitoken.SourceAllAccountContent {
		query := `
SELECT m.id, m.display_name, m.storage_kind, mg.permission, m.mode
FROM mount_grants mg
JOIN mounts m ON m.id = mg.mount_id
WHERE mg.account_id = ?
  AND mg.permission IN ('viewer', 'editor')
  AND m.purpose = 'common'
  AND m.storage_kind = 'external'
  AND m.status = 'active'
  AND (? = '' OR m.id = ?)
ORDER BY m.display_name, m.id`
		rows, err := s.db.QueryContext(ctx, query, subject.AccountID, mountID, mountID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var record mountRecord
			record.source = contentref.SourceCommonMount
			if err := rows.Scan(&record.mount.ID, &record.mount.Name, &record.mount.StorageKind, &record.mount.Permission, &record.mount.Mode); err != nil {
				rows.Close()
				return nil, err
			}
			record.mount.ReadOnly = record.mount.Mode == domain.MountModeReadOnly
			candidates = append(candidates, record)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	records := make([]mountRecord, 0, len(candidates))
	for _, candidate := range candidates {
		paths := currentBoundaryPaths(subject.Principal, candidate.source, candidate.mount.ID)
		for _, boundary := range paths {
			locator := access.Locator{Source: candidate.source, MountID: candidate.mount.ID, Path: boundary}
			if candidate.source == contentref.SourcePersonal {
				locator.MountID = ""
			}
			authorized, err := s.guard.Authorize(ctx, access.CheckRequest{
				Subject: subject, Scope: scope, Locator: locator,
				RequiredPermission: domain.ContentPermissionViewer,
			})
			if err != nil {
				continue
			}
			candidate.authorized = authorized
			candidate.boundaries = append(candidate.boundaries, authorized.RelativePath)
		}
		if len(candidate.boundaries) > 0 {
			records = append(records, candidate)
		}
	}
	return records, nil
}

func currentBoundaryPaths(principal *aitoken.Principal, source contentref.Source, mountID string) []string {
	if principal == nil {
		return []string{"."}
	}
	seen := map[string]bool{}
	paths := make([]string, 0)
	for _, boundary := range principal.Boundaries {
		if boundary.Source == aitoken.SourceAllAccountContent {
			if !seen["."] {
				paths = append(paths, ".")
				seen["."] = true
			}
			continue
		}
		if boundary.Source != source || source == contentref.SourceCommonMount && boundary.MountID != mountID {
			continue
		}
		value := boundary.RelativePath
		if value == "" {
			value = "."
		}
		if !seen[value] {
			paths = append(paths, value)
			seen[value] = true
		}
	}
	return paths
}
