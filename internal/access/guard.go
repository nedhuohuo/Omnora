package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/storage"
)

var (
	ErrInvalidRequest            = errors.New("invalid access request")
	ErrUnauthorized              = errors.New("unauthorized")
	ErrForbidden                 = errors.New("forbidden")
	ErrBoundaryViolation         = errors.New("boundary_violation")
	ErrReadonlyMount             = errors.New("readonly_mount")
	ErrMountUnavailable          = errors.New("mount_unavailable")
	ErrMountIdentityUnverifiable = mountid.ErrIdentityUnverifiable
)

// Locator identifies a mount-relative object. Path must never be a host path.
type Locator struct {
	SpaceID string
	MountID string
	Path    string
}

// Subject is either a browser session (Principal nil) or an AI-token subject.
type Subject struct {
	AccountID string
	Principal *aitoken.Principal
}

type CheckRequest struct {
	Subject            Subject
	Scope              aitoken.Scope
	Locator            Locator
	RequiredPermission domain.SpacePermission
	Write              bool
}

type AuthorizedMount struct {
	ID           string
	SpaceID      string
	Root         string
	Kind         string
	Mode         domain.MountMode
	IdentityJSON string
	RelativePath string
}

type AuthorizedPair struct {
	Source      AuthorizedMount
	Destination AuthorizedMount
}

type Guard struct {
	db *sql.DB
}

func NewGuard(db *sql.DB) *Guard {
	return &Guard{db: db}
}

// Authorize performs a live account, token, ACL, mount, boundary, path and
// mount-identity check. No authorization result is cached.
func (g *Guard) Authorize(ctx context.Context, req CheckRequest) (AuthorizedMount, error) {
	if g == nil || g.db == nil {
		return AuthorizedMount{}, ErrUnauthorized
	}
	accountID := strings.TrimSpace(req.Subject.AccountID)
	if accountID == "" || strings.TrimSpace(req.Locator.SpaceID) == "" || strings.TrimSpace(req.Locator.MountID) == "" {
		return AuthorizedMount{}, ErrInvalidRequest
	}

	principal := req.Subject.Principal
	if principal != nil {
		if principal.AccountID != accountID || strings.TrimSpace(principal.TokenID) == "" {
			return AuthorizedMount{}, ErrUnauthorized
		}
		fresh, err := aitoken.NewService(g.db).RefreshPrincipal(ctx, principal.TokenID)
		if err != nil {
			return AuthorizedMount{}, fmt.Errorf("%w: token is not valid: %v", ErrUnauthorized, err)
		}
		if fresh.AccountID != accountID || !fresh.HasScope(req.Scope) {
			if !fresh.HasScope(req.Scope) {
				return AuthorizedMount{}, fmt.Errorf("%w: scope is not allowed", ErrForbidden)
			}
			return AuthorizedMount{}, ErrUnauthorized
		}
		principal = &fresh
	}

	if err := g.requireActiveAccount(ctx, accountID); err != nil {
		return AuthorizedMount{}, err
	}
	permission, err := g.spacePermission(ctx, accountID, req.Locator.SpaceID)
	if err != nil {
		return AuthorizedMount{}, err
	}
	if !permissionAllows(permission, req.RequiredPermission) {
		return AuthorizedMount{}, fmt.Errorf("%w: insufficient space permission", ErrForbidden)
	}
	if req.Write && !permissionAllows(permission, domain.SpacePermissionEditor) {
		return AuthorizedMount{}, fmt.Errorf("%w: write requires editor permission", ErrForbidden)
	}

	mount, err := g.loadMount(ctx, req.Locator.SpaceID, req.Locator.MountID)
	if err != nil {
		return AuthorizedMount{}, err
	}
	if req.Write && mount.Mode != domain.MountModeReadWrite {
		return AuthorizedMount{}, ErrReadonlyMount
	}

	cleaned, err := cleanLocatorPath(req.Locator.Path)
	if err != nil {
		return AuthorizedMount{}, fmt.Errorf("%w: %v", ErrBoundaryViolation, err)
	}
	if principal != nil && !withinBoundaries(*principal, req.Locator.SpaceID, req.Locator.MountID, cleaned) {
		return AuthorizedMount{}, ErrBoundaryViolation
	}
	if err := rejectSymlinkPath(mount.Root, cleaned); err != nil {
		return AuthorizedMount{}, err
	}
	if err := g.VerifyMountIdentity(ctx, mount); err != nil {
		return AuthorizedMount{}, err
	}
	mount.RelativePath = cleaned
	return mount, nil
}

func (g *Guard) AuthorizePair(ctx context.Context, source, destination CheckRequest) (AuthorizedPair, error) {
	src, err := g.Authorize(ctx, source)
	if err != nil {
		return AuthorizedPair{}, fmt.Errorf("source: %w", err)
	}
	dst, err := g.Authorize(ctx, destination)
	if err != nil {
		return AuthorizedPair{}, fmt.Errorf("destination: %w", err)
	}
	return AuthorizedPair{Source: src, Destination: dst}, nil
}

// LoadMount and VerifyMountIdentity are compatibility helpers for existing
// handlers. They intentionally contain no ACL or token authorization logic.
func (g *Guard) LoadMount(ctx context.Context, spaceID, mountID string) (AuthorizedMount, error) {
	if g == nil || g.db == nil {
		return AuthorizedMount{}, ErrUnauthorized
	}
	return g.loadMount(ctx, spaceID, mountID)
}

func (g *Guard) VerifyMountIdentity(ctx context.Context, mount AuthorizedMount) error {
	if g == nil || g.db == nil || strings.TrimSpace(mount.ID) == "" {
		return ErrMountIdentityUnverifiable
	}
	if strings.TrimSpace(mount.IdentityJSON) == "" {
		_ = g.markUnavailable(ctx, mount.ID)
		return ErrMountIdentityUnverifiable
	}
	var stored mountid.Identity
	if err := json.Unmarshal([]byte(mount.IdentityJSON), &stored); err != nil {
		_ = g.markUnavailable(ctx, mount.ID)
		return fmt.Errorf("%w: %v", ErrMountIdentityUnverifiable, err)
	}
	current, err := mountid.Capture(mount.Root)
	if err != nil {
		_ = g.markUnavailable(ctx, mount.ID)
		return fmt.Errorf("%w: %v", ErrMountIdentityUnverifiable, err)
	}
	if !MountIdentityMatches(stored, current) {
		_ = g.markUnavailable(ctx, mount.ID)
		return fmt.Errorf("%w: mount root identity drifted", ErrMountIdentityUnverifiable)
	}
	if MountIdentityNeedsRefresh(stored, current) {
		_ = g.refreshIdentity(ctx, mount.ID, current)
	}
	return nil
}

func (g *Guard) HasSpacePermission(ctx context.Context, accountID, spaceID string, required domain.SpacePermission) bool {
	if g == nil || g.db == nil {
		return false
	}
	permission, err := g.spacePermission(ctx, accountID, spaceID)
	if err != nil {
		return false
	}
	return permissionAllows(permission, required)
}

func (g *Guard) requireActiveAccount(ctx context.Context, accountID string) error {
	var status string
	if err := g.db.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id = ?`, accountID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		return err
	}
	if status != "active" {
		return ErrUnauthorized
	}
	return nil
}

func (g *Guard) spacePermission(ctx context.Context, accountID, spaceID string) (domain.SpacePermission, error) {
	var permission domain.SpacePermission
	err := g.db.QueryRowContext(ctx, `
SELECT sm.permission
FROM space_members sm
JOIN spaces sp ON sp.id = sm.space_id
WHERE sm.account_id = ? AND sm.space_id = ? AND sp.status = 'active'
`, accountID, spaceID).Scan(&permission)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SpacePermissionNone, ErrForbidden
	}
	if err != nil {
		return domain.SpacePermissionNone, err
	}
	return permission, nil
}

func (g *Guard) loadMount(ctx context.Context, spaceID, mountID string) (AuthorizedMount, error) {
	var mount AuthorizedMount
	var status string
	err := g.db.QueryRowContext(ctx, `
SELECT id, space_id, root_path, kind, mode, COALESCE(mount_identity_json, ''), status
FROM mounts
WHERE id = ? AND space_id = ? AND status <> 'deleted'
`, mountID, spaceID).Scan(&mount.ID, &mount.SpaceID, &mount.Root, &mount.Kind, &mount.Mode, &mount.IdentityJSON, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizedMount{}, sql.ErrNoRows
	}
	if err != nil {
		return AuthorizedMount{}, err
	}
	if status != "active" {
		return AuthorizedMount{}, ErrMountUnavailable
	}
	return mount, nil
}

func (g *Guard) markUnavailable(ctx context.Context, mountID string) error {
	_, err := g.db.ExecContext(ctx, `
UPDATE mounts SET status = 'unavailable', updated_at = ?
WHERE id = ? AND status = 'active'
`, time.Now().UTC().Format(time.RFC3339Nano), mountID)
	return err
}

func (g *Guard) refreshIdentity(ctx context.Context, mountID string, identity mountid.Identity) error {
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	_, err = g.db.ExecContext(ctx, `
UPDATE mounts SET mount_identity_json = ?, updated_at = ?
WHERE id = ? AND status = 'active'
`, string(identityJSON), time.Now().UTC().Format(time.RFC3339Nano), mountID)
	return err
}

func permissionAllows(permission, required domain.SpacePermission) bool {
	switch required {
	case domain.SpacePermissionViewer:
		return accessRank(permission) >= accessRank(domain.SpacePermissionViewer)
	case domain.SpacePermissionEditor:
		return accessRank(permission) >= accessRank(domain.SpacePermissionEditor)
	case domain.SpacePermissionManager:
		return permission == domain.SpacePermissionManager
	default:
		return false
	}
}

func accessRank(permission domain.SpacePermission) int {
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

func withinBoundaries(principal aitoken.Principal, spaceID, mountID, cleaned string) bool {
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

func cleanLocatorPath(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) || strings.Contains(raw, "\\") {
		return "", errors.New("absolute paths are not allowed")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", errors.New("path traversal is not allowed")
		}
	}
	return storage.CleanRelativePath(raw)
}

func rejectSymlinkPath(root, cleaned string) error {
	if strings.TrimSpace(root) == "" {
		return ErrMountIdentityUnverifiable
	}
	if cleaned == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: could not inspect path", ErrBoundaryViolation)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not allowed", ErrBoundaryViolation)
		}
	}
	return nil
}

// MountIdentityMatches compares durable filesystem identity. Kernel mount IDs
// are intentionally ignored because they can change when a container restarts.
func MountIdentityMatches(stored, current mountid.Identity) bool {
	if filepath.Clean(stored.Path) != filepath.Clean(current.Path) || stored.Device != current.Device || stored.Inode != current.Inode {
		return false
	}
	if stored.Statx.Available || current.Statx.Available {
		if stored.Statx.Available != current.Statx.Available ||
			stored.Statx.DeviceMajor != current.Statx.DeviceMajor ||
			stored.Statx.DeviceMinor != current.Statx.DeviceMinor ||
			stored.Statx.Inode != current.Statx.Inode {
			return false
		}
	}
	if stored.Mount.Available != current.Mount.Available {
		return false
	}
	if !stored.Mount.Available {
		return true
	}
	return stored.Mount.Device == current.Mount.Device &&
		stored.Mount.Root == current.Mount.Root &&
		stored.Mount.Point == current.Mount.Point &&
		stored.Mount.FSType == current.Mount.FSType &&
		stored.Mount.Source == current.Mount.Source
}

func MountIdentityNeedsRefresh(stored, current mountid.Identity) bool {
	return stored.Statx.MountID != current.Statx.MountID ||
		(stored.Mount.Available && current.Mount.Available && stored.Mount.ID != current.Mount.ID)
}
