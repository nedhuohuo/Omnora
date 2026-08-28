package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"omnora/internal/domain"
)

var ErrMountAccessDenied = errors.New("mount access denied")

// MountDecision is the complete current authorization state for an account and
// mount. Callers must still verify the filesystem identity before opening data.
type MountDecision struct {
	AccountID           string
	SpaceID             string
	MountID             string
	SpacePermission     domain.SpacePermission
	GrantPermission     domain.SpacePermission
	EffectivePermission domain.SpacePermission
	MountMode           domain.MountMode
	AllowPublicShares   bool
}

type MountService struct {
	db *sql.DB
}

func NewMountService(db *sql.DB) *MountService {
	return &MountService{db: db}
}

func (s *MountService) Authorize(ctx context.Context, accountID, spaceID, mountID string, operation Operation) (MountDecision, error) {
	if s == nil || s.db == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(spaceID) == "" || strings.TrimSpace(mountID) == "" {
		return MountDecision{}, ErrMountAccessDenied
	}

	var decision MountDecision
	var allowPublicShares int
	err := s.db.QueryRowContext(ctx, `
SELECT a.id, sp.id, m.id, sm.permission, mg.permission, m.mode, m.allow_public_shares
FROM accounts a
JOIN space_members sm ON sm.account_id = a.id
JOIN spaces sp ON sp.id = sm.space_id
JOIN mounts m ON m.space_id = sp.id
JOIN mount_account_grants mg ON mg.mount_id = m.id AND mg.account_id = a.id
WHERE a.id = ? AND sp.id = ? AND m.id = ?
  AND a.status = 'active' AND sp.status = 'active' AND m.status = 'active'
`, accountID, spaceID, mountID).Scan(
		&decision.AccountID,
		&decision.SpaceID,
		&decision.MountID,
		&decision.SpacePermission,
		&decision.GrantPermission,
		&decision.MountMode,
		&allowPublicShares,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MountDecision{}, ErrMountAccessDenied
	}
	if err != nil {
		return MountDecision{}, err
	}
	decision.AllowPublicShares = allowPublicShares == 1
	decision.EffectivePermission = minimumPermission(decision.SpacePermission, decision.GrantPermission)

	if !allowsMountDecision(decision, operation) {
		return MountDecision{}, ErrMountAccessDenied
	}
	return decision, nil
}

func (s *MountService) AllowedMountIDs(ctx context.Context, accountID, spaceID string, operation Operation) ([]string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(spaceID) == "" {
		return []string{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, sm.permission, mg.permission, m.mode, m.allow_public_shares
FROM accounts a
JOIN space_members sm ON sm.account_id = a.id
JOIN spaces sp ON sp.id = sm.space_id
JOIN mounts m ON m.space_id = sp.id
JOIN mount_account_grants mg ON mg.mount_id = m.id AND mg.account_id = a.id
WHERE a.id = ? AND sp.id = ?
  AND a.status = 'active' AND sp.status = 'active' AND m.status <> 'deleted'
ORDER BY m.display_name, m.id
`, accountID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var decision MountDecision
		var allowPublicShares int
		if err := rows.Scan(&decision.MountID, &decision.SpacePermission, &decision.GrantPermission, &decision.MountMode, &allowPublicShares); err != nil {
			return nil, err
		}
		decision.AllowPublicShares = allowPublicShares == 1
		decision.EffectivePermission = minimumPermission(decision.SpacePermission, decision.GrantPermission)
		if allowsMountDecision(decision, operation) {
			ids = append(ids, decision.MountID)
		}
	}
	return ids, rows.Err()
}

func allowsMountDecision(decision MountDecision, operation Operation) bool {
	switch operation {
	case OperationRead:
		return Allows(decision.EffectivePermission, decision.MountMode, OperationRead)
	case OperationWrite:
		return Allows(decision.EffectivePermission, decision.MountMode, OperationWrite)
	case OperationManageShare:
		return Allows(decision.EffectivePermission, decision.MountMode, OperationManageShare) && decision.AllowPublicShares
	case OperationManageACL:
		return Allows(decision.EffectivePermission, decision.MountMode, OperationManageACL)
	default:
		return false
	}
}

func minimumPermission(left, right domain.SpacePermission) domain.SpacePermission {
	if permissionRank(left) <= permissionRank(right) {
		return left
	}
	return right
}

func permissionRank(permission domain.SpacePermission) int {
	switch permission {
	case domain.SpacePermissionViewer:
		return 1
	case domain.SpacePermissionEditor:
		return 2
	case domain.SpacePermissionManager:
		return 3
	default:
		return 0
	}
}
