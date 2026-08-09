package mountadmin

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"omnora/internal/domain"
)

var (
	ErrNotFound             = errors.New("mount not found")
	ErrInvalidInput         = errors.New("invalid mount input")
	ErrRestrictedGovernance = errors.New("restricted mount governance requires the initial administrator")
	ErrMountUnavailable     = errors.New("mount unavailable")
	ErrMountConflict        = errors.New("mount conflict")
	ErrIdentityUnverifiable = errors.New("mount identity unverifiable")
	ErrRootNotAllowed       = errors.New("mount root is not allowed")
	ErrNotWritable          = errors.New("mount root is not writable")
	ErrConfirmationRequired = errors.New("mount name confirmation is required")
)

type Mount struct {
	ID           string
	DisplayName  string
	RootPath     string
	Governance   domain.MountGovernance
	Mode         domain.MountMode
	IndexEnabled bool
	ShareEnabled bool
	Status       string
	GrantCount   int
}

type Service struct {
	db           *sql.DB
	externalRoot string
}

func New(db *sql.DB, externalRoot string) *Service {
	return &Service{db: db, externalRoot: filepath.Clean(strings.TrimSpace(externalRoot))}
}

func (s *Service) isInitialAdmin(ctx context.Context, accountID string) (bool, error) {
	var initialID string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system_state WHERE key = 'initial_admin_account_id'`).Scan(&initialID)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM accounts ORDER BY created_at ASC, id ASC LIMIT 1`).Scan(&initialID)
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(initialID) == strings.TrimSpace(accountID), nil
}

func (s *Service) ListMounts(ctx context.Context, accountID string) ([]Mount, error) {
	initial, err := s.isInitialAdmin(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.display_name, m.root_path, m.governance, m.mode,
       m.index_enabled, m.share_enabled, m.status, COUNT(mg.account_id)
FROM mounts m
LEFT JOIN mount_grants mg ON mg.mount_id = m.id
WHERE m.purpose = 'common'
  AND m.status <> 'deleted'
  AND (m.governance = 'normal' OR ? = 1)
GROUP BY m.id
ORDER BY lower(m.display_name), m.id
`, initial)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Mount, 0)
	for rows.Next() {
		item, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) LoadMount(ctx context.Context, accountID, mountID string) (Mount, error) {
	initial, err := s.isInitialAdmin(ctx, accountID)
	if err != nil {
		return Mount{}, err
	}
	var item Mount
	var indexEnabled, shareEnabled int
	var grantCount int
	err = s.db.QueryRowContext(ctx, `
SELECT m.id, m.display_name, m.root_path, m.governance, m.mode,
       m.index_enabled, m.share_enabled, m.status,
       (SELECT COUNT(1) FROM mount_grants mg WHERE mg.mount_id = m.id)
FROM mounts m
WHERE m.id = ?
  AND m.purpose = 'common'
  AND m.status <> 'deleted'
  AND (m.governance = 'normal' OR ? = 1)
`, strings.TrimSpace(mountID), initial).Scan(
		&item.ID, &item.DisplayName, &item.RootPath, &item.Governance, &item.Mode,
		&indexEnabled, &shareEnabled, &item.Status, &grantCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Mount{}, ErrNotFound
	}
	if err != nil {
		return Mount{}, err
	}
	item.IndexEnabled = indexEnabled == 1
	item.ShareEnabled = shareEnabled == 1
	item.GrantCount = grantCount
	return item, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMount(row scanner) (Mount, error) {
	var item Mount
	var indexEnabled, shareEnabled int
	if err := row.Scan(
		&item.ID, &item.DisplayName, &item.RootPath, &item.Governance, &item.Mode,
		&indexEnabled, &shareEnabled, &item.Status, &item.GrantCount,
	); err != nil {
		return Mount{}, err
	}
	item.IndexEnabled = indexEnabled == 1
	item.ShareEnabled = shareEnabled == 1
	return item, nil
}
