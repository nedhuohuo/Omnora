package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/mountid"
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

// Locator is the account/mount content coordinate shared by member and MCP
// APIs. Automation authorization deliberately rejects collaboration locators.
type Locator = contentref.Locator

// Subject is either a browser session (Principal nil) or an AI-token subject.
type Subject struct {
	AccountID string
	Principal *aitoken.Principal
}

type CheckRequest struct {
	Subject            Subject
	Scope              aitoken.Scope
	Locator            Locator
	RequiredPermission domain.ContentPermission
	Write              bool
}

// AuthorizedMount separates the caller-visible source path from its unique
// physical path inside the mount. Root is cropped to the account for personal
// content; MountRoot is always the directory whose identity is verified.
type AuthorizedMount struct {
	Source              contentref.Source
	ID                  string
	Root                string
	MountRoot           string
	StorageKind         domain.StorageKind
	Mode                domain.MountMode
	IdentityJSON        string
	RelativePath        string
	StorageRelativePath string
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

// Authorize reloads token state, account state, content grants and mount
// identity on every call. No authorization result is cached.
func (g *Guard) Authorize(ctx context.Context, req CheckRequest) (AuthorizedMount, error) {
	if g == nil || g.db == nil {
		return AuthorizedMount{}, ErrUnauthorized
	}
	accountID := strings.TrimSpace(req.Subject.AccountID)
	if accountID == "" || !req.RequiredPermission.Valid() {
		return AuthorizedMount{}, ErrInvalidRequest
	}
	locator, err := contentref.NormalizeForAutomation(req.Locator)
	if err != nil {
		if errors.Is(err, contentref.ErrInvalidLocator) {
			return AuthorizedMount{}, fmt.Errorf("%w: %v", ErrBoundaryViolation, err)
		}
		return AuthorizedMount{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	principal, err := g.refreshPrincipal(ctx, accountID, req.Subject.Principal, req.Scope)
	if err != nil {
		return AuthorizedMount{}, err
	}
	if err := g.requireActiveAccount(ctx, accountID); err != nil {
		return AuthorizedMount{}, err
	}

	var mount AuthorizedMount
	var permission domain.ContentPermission
	switch locator.Source {
	case contentref.SourcePersonal:
		mount, permission, err = g.authorizePersonal(ctx, accountID)
	case contentref.SourceCommonMount:
		mount, permission, err = g.authorizeCommon(ctx, accountID, locator.MountID)
	default:
		return AuthorizedMount{}, ErrInvalidRequest
	}
	if err != nil {
		return AuthorizedMount{}, err
	}
	if !permission.Allows(req.RequiredPermission) || req.Write && permission != domain.ContentPermissionEditor {
		return AuthorizedMount{}, ErrForbidden
	}
	if req.Write && mount.Mode != domain.MountModeReadWrite {
		return AuthorizedMount{}, ErrReadonlyMount
	}
	if principal != nil && !withinBoundaries(*principal, locator, locator.Path) {
		return AuthorizedMount{}, ErrBoundaryViolation
	}
	if err := g.VerifyMountIdentity(ctx, mount); err != nil {
		return AuthorizedMount{}, err
	}
	if mount.Source == contentref.SourcePersonal {
		accountPath, err := cleanAccountStoragePath(accountID)
		if err != nil {
			return AuthorizedMount{}, err
		}
		if err := rejectSymlinkPath(mount.MountRoot, accountPath); err != nil {
			return AuthorizedMount{}, err
		}
		if err := requireRealDirectory(mount.Root); err != nil {
			return AuthorizedMount{}, err
		}
	}
	if err := rejectSymlinkPath(mount.Root, locator.Path); err != nil {
		return AuthorizedMount{}, err
	}

	mount.RelativePath = locator.Path
	if mount.Source == contentref.SourcePersonal {
		mount.StorageRelativePath = path.Join(accountID, locator.Path)
	} else {
		mount.StorageRelativePath = locator.Path
	}
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

// LoadMountIdentity loads only mount classification and identity metadata.
// It performs no account authorization and accepts no legacy Space coordinate.
func (g *Guard) LoadMountIdentity(ctx context.Context, mountID string) (AuthorizedMount, error) {
	if g == nil || g.db == nil || strings.TrimSpace(mountID) == "" {
		return AuthorizedMount{}, ErrInvalidRequest
	}
	var mount AuthorizedMount
	var rootPath string
	var purpose domain.MountPurpose
	var status string
	err := g.db.QueryRowContext(ctx, `
SELECT id, root_path, purpose, storage_kind, mode, COALESCE(mount_identity_json, ''), status
FROM mounts
WHERE id = ? AND status <> 'deleted'
`, strings.TrimSpace(mountID)).Scan(&mount.ID, &rootPath, &purpose, &mount.StorageKind, &mount.Mode, &mount.IdentityJSON, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizedMount{}, ErrMountUnavailable
	}
	if err != nil {
		return AuthorizedMount{}, err
	}
	if status != "active" {
		return AuthorizedMount{}, ErrMountUnavailable
	}
	switch purpose {
	case domain.MountPurposePersonalDefault:
		if mount.StorageKind != domain.StorageKindManaged || rootPath != "personal" || mount.Mode != domain.MountModeReadWrite {
			return AuthorizedMount{}, ErrMountUnavailable
		}
		mount.Source = contentref.SourcePersonal
	case domain.MountPurposeCommon:
		if mount.StorageKind != domain.StorageKindExternal {
			return AuthorizedMount{}, ErrMountUnavailable
		}
		mount.Source = contentref.SourceCommonMount
	default:
		return AuthorizedMount{}, ErrMountUnavailable
	}
	mountRoot, err := mountRootFromIdentity(rootPath, mount.Source, mount.IdentityJSON)
	if err != nil {
		_ = g.markUnavailable(ctx, mount.ID)
		return AuthorizedMount{}, err
	}
	mount.Root = mountRoot
	mount.MountRoot = mountRoot
	return mount, nil
}

// VerifyMountIdentity checks the full mount root, never an account-cropped
// personal directory. Malformed or drifted identity marks the mount unavailable.
func (g *Guard) VerifyMountIdentity(ctx context.Context, mount AuthorizedMount) error {
	if g == nil || g.db == nil || strings.TrimSpace(mount.ID) == "" || strings.TrimSpace(mount.MountRoot) == "" || strings.TrimSpace(mount.IdentityJSON) == "" {
		if g != nil && g.db != nil && strings.TrimSpace(mount.ID) != "" {
			_ = g.markUnavailable(ctx, mount.ID)
		}
		return ErrMountIdentityUnverifiable
	}
	if err := requireRealDirectory(mount.MountRoot); err != nil {
		_ = g.markUnavailable(ctx, mount.ID)
		return ErrMountIdentityUnverifiable
	}
	var stored mountid.Identity
	if err := json.Unmarshal([]byte(mount.IdentityJSON), &stored); err != nil || filepath.Clean(stored.Path) != filepath.Clean(mount.MountRoot) {
		_ = g.markUnavailable(ctx, mount.ID)
		return ErrMountIdentityUnverifiable
	}
	current, err := mountid.Capture(mount.MountRoot)
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

func (g *Guard) refreshPrincipal(ctx context.Context, accountID string, presented *aitoken.Principal, scope aitoken.Scope) (*aitoken.Principal, error) {
	if presented == nil {
		return nil, nil
	}
	if presented.AccountID != accountID || strings.TrimSpace(presented.TokenID) == "" || strings.TrimSpace(string(scope)) == "" {
		return nil, ErrUnauthorized
	}
	fresh, err := aitoken.NewService(g.db).RefreshPrincipal(ctx, presented.TokenID)
	if err != nil || fresh.AccountID != accountID || strings.TrimSpace(fresh.TokenID) == "" || len(fresh.Boundaries) == 0 {
		return nil, ErrUnauthorized
	}
	if !fresh.HasScope(scope) {
		return nil, ErrForbidden
	}
	return &fresh, nil
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

func (g *Guard) authorizePersonal(ctx context.Context, accountID string) (AuthorizedMount, domain.ContentPermission, error) {
	var mountID, rootPath, identityJSON string
	var storageKind domain.StorageKind
	var mode domain.MountMode
	err := g.db.QueryRowContext(ctx, `
SELECT m.id, m.root_path, m.storage_kind, m.mode, COALESCE(m.mount_identity_json, '')
FROM personal_directories pd
JOIN mounts m ON m.purpose = 'personal_default'
WHERE pd.account_id = ?
  AND pd.relative_path = pd.account_id
  AND pd.state = 'ready'
  AND m.root_path = 'personal'
  AND m.storage_kind = 'managed'
  AND m.governance = 'system'
  AND m.mode = 'read_write'
  AND m.status = 'active'
`, accountID).Scan(&mountID, &rootPath, &storageKind, &mode, &identityJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizedMount{}, domain.ContentPermissionNone, ErrMountUnavailable
	}
	if err != nil {
		return AuthorizedMount{}, domain.ContentPermissionNone, err
	}
	mountRoot, err := mountRootFromIdentity(rootPath, contentref.SourcePersonal, identityJSON)
	if err != nil {
		_ = g.markUnavailable(ctx, mountID)
		return AuthorizedMount{}, domain.ContentPermissionNone, err
	}
	return AuthorizedMount{
		Source:       contentref.SourcePersonal,
		ID:           mountID,
		Root:         filepath.Join(mountRoot, accountID),
		MountRoot:    mountRoot,
		StorageKind:  storageKind,
		Mode:         mode,
		IdentityJSON: identityJSON,
	}, domain.ContentPermissionEditor, nil
}

func (g *Guard) authorizeCommon(ctx context.Context, accountID, mountID string) (AuthorizedMount, domain.ContentPermission, error) {
	var mount AuthorizedMount
	var rootPath string
	var permission domain.ContentPermission
	err := g.db.QueryRowContext(ctx, `
SELECT m.id, m.root_path, m.storage_kind, m.mode, COALESCE(m.mount_identity_json, ''), mg.permission
FROM mount_grants mg
JOIN mounts m ON m.id = mg.mount_id
WHERE mg.account_id = ?
  AND mg.mount_id = ?
  AND mg.permission IN ('viewer', 'editor')
  AND m.purpose = 'common'
  AND m.storage_kind = 'external'
  AND m.status = 'active'
`, accountID, mountID).Scan(&mount.ID, &rootPath, &mount.StorageKind, &mount.Mode, &mount.IdentityJSON, &permission)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizedMount{}, domain.ContentPermissionNone, ErrForbidden
	}
	if err != nil {
		return AuthorizedMount{}, domain.ContentPermissionNone, err
	}
	mountRoot, err := mountRootFromIdentity(rootPath, contentref.SourceCommonMount, mount.IdentityJSON)
	if err != nil {
		_ = g.markUnavailable(ctx, mount.ID)
		return AuthorizedMount{}, domain.ContentPermissionNone, err
	}
	mount.Source = contentref.SourceCommonMount
	mount.Root = mountRoot
	mount.MountRoot = mountRoot
	return mount, permission, nil
}

func mountRootFromIdentity(rootPath string, source contentref.Source, identityJSON string) (string, error) {
	if strings.TrimSpace(identityJSON) == "" {
		return "", ErrMountIdentityUnverifiable
	}
	var identity mountid.Identity
	if err := json.Unmarshal([]byte(identityJSON), &identity); err != nil || !filepath.IsAbs(identity.Path) {
		return "", ErrMountIdentityUnverifiable
	}
	identityRoot := filepath.Clean(identity.Path)
	switch source {
	case contentref.SourcePersonal:
		if rootPath != "personal" || filepath.Base(identityRoot) != rootPath {
			return "", ErrMountIdentityUnverifiable
		}
	case contentref.SourceCommonMount:
		if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != identityRoot {
			return "", ErrMountIdentityUnverifiable
		}
	default:
		return "", ErrMountIdentityUnverifiable
	}
	return identityRoot, nil
}

func cleanAccountStoragePath(accountID string) (string, error) {
	if accountID == "" || filepath.IsAbs(accountID) || strings.ContainsAny(accountID, `/\\`) || accountID == "." || accountID == ".." {
		return "", ErrBoundaryViolation
	}
	return accountID, nil
}

func withinBoundaries(principal aitoken.Principal, locator Locator, cleaned string) bool {
	for _, boundary := range principal.Boundaries {
		if boundary.Source == aitoken.SourceAllAccountContent {
			return true
		}
		if boundary.Source != locator.Source {
			continue
		}
		if locator.Source == contentref.SourcePersonal && boundary.MountID != "" {
			continue
		}
		if locator.Source == contentref.SourceCommonMount && boundary.MountID != locator.MountID {
			continue
		}
		boundaryPath := strings.TrimSpace(boundary.RelativePath)
		if boundaryPath == "" {
			boundaryPath = "."
		}
		if pathWithin(cleaned, boundaryPath) {
			return true
		}
	}
	return false
}

func pathWithin(candidate, boundary string) bool {
	return boundary == "." || candidate == boundary || strings.HasPrefix(candidate, boundary+"/")
}

func requireRealDirectory(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrMountIdentityUnverifiable
	}
	return nil
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
