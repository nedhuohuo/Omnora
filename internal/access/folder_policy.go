package access

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"omnora/internal/aitoken"
)

var (
	ErrFolderDenied                 = errors.New("folder_denied")
	ErrFolderDenylistReviewRequired = errors.New("folder_denylist_review_required")
)

// FolderPolicy provides path-only deny checks after a principal has been
// refreshed. Callers that touch the filesystem must still enter through Guard
// so the persisted directory identities are verified before use.
type FolderPolicy struct {
	deniedPaths    []string
	reviewRequired bool
}

// NewFolderPolicy extracts one mount's deny rules from a space grant. It does
// not accept host paths and fails closed if persisted policy metadata is not
// usable.
func NewFolderPolicy(grant aitoken.SpaceGrant, mountID string) (FolderPolicy, error) {
	mountID = strings.TrimSpace(mountID)
	if mountID == "" {
		return FolderPolicy{}, ErrInvalidRequest
	}
	policy := FolderPolicy{}
	for _, deny := range grant.FolderDenylist {
		if deny.MountID != mountID {
			continue
		}
		cleaned, err := cleanLocatorPath(deny.RelativePath)
		if err != nil || cleaned == "." || strings.TrimSpace(deny.IdentityFingerprint) == "" {
			return FolderPolicy{}, ErrFolderDenylistReviewRequired
		}
		switch deny.Status {
		case aitoken.FolderDenyStatusActive:
			policy.deniedPaths = append(policy.deniedPaths, cleaned)
		case aitoken.FolderDenyStatusNeedsReview:
			policy.reviewRequired = true
		default:
			return FolderPolicy{}, ErrFolderDenylistReviewRequired
		}
	}
	return policy, nil
}

// CheckPath rejects a denied directory and every descendant. An ancestor of a
// deny remains readable so directory listing code can enter it and filter each
// child through this same method.
func (p FolderPolicy) CheckPath(relativePath string) error {
	cleaned, err := cleanLocatorPath(relativePath)
	if err != nil {
		return fmt.Errorf("%w: invalid relative path", ErrBoundaryViolation)
	}
	if p.reviewRequired {
		return ErrFolderDenylistReviewRequired
	}
	for _, deniedPath := range p.deniedPaths {
		if pathAtOrBelow(cleaned, deniedPath) {
			return ErrFolderDenied
		}
	}
	return nil
}

// CheckSubtree applies CheckPath and also rejects recursive operations whose
// requested parent contains a denied descendant.
func (p FolderPolicy) CheckSubtree(relativePath string) error {
	cleaned, err := cleanLocatorPath(relativePath)
	if err != nil {
		return fmt.Errorf("%w: invalid relative path", ErrBoundaryViolation)
	}
	if p.reviewRequired {
		return ErrFolderDenylistReviewRequired
	}
	for _, deniedPath := range p.deniedPaths {
		if pathAtOrBelow(cleaned, deniedPath) || pathAtOrBelow(deniedPath, cleaned) {
			return ErrFolderDenied
		}
	}
	return nil
}

func pathAtOrBelow(candidate, parent string) bool {
	return parent == "." || candidate == parent || strings.HasPrefix(candidate, parent+"/")
}

func (g *Guard) enforceFolderPolicy(
	ctx context.Context,
	principal aitoken.Principal,
	grant aitoken.SpaceGrant,
	mount AuthorizedMount,
	cleanedPath string,
	recursive bool,
) error {
	policy, err := NewFolderPolicy(grant, mount.ID)
	if err != nil {
		return err
	}
	if policy.reviewRequired {
		return ErrFolderDenylistReviewRequired
	}
	for _, deny := range grant.FolderDenylist {
		if deny.MountID != mount.ID {
			continue
		}
		fingerprint, fingerprintErr := captureFolderDenyFingerprint(mount.Root, deny.RelativePath)
		if deny.Status != aitoken.FolderDenyStatusActive ||
			fingerprintErr != nil ||
			fingerprint != strings.TrimSpace(deny.IdentityFingerprint) {
			if markErr := g.markFolderDenyNeedsReview(ctx, principal.TokenID, grant.SpaceID, mount.ID, deny.RelativePath); markErr != nil {
				return markErr
			}
			return ErrFolderDenylistReviewRequired
		}
	}
	if recursive {
		return policy.CheckSubtree(cleanedPath)
	}
	return policy.CheckPath(cleanedPath)
}

func captureFolderDenyFingerprint(root, relativePath string) (string, error) {
	cleaned, err := cleanLocatorPath(relativePath)
	if err != nil || cleaned == "." {
		return "", ErrFolderDenylistReviewRequired
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return "", ErrFolderDenylistReviewRequired
	}
	current := root
	info, err := os.Lstat(current)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrFolderDenylistReviewRequired
	}
	for _, component := range strings.Split(cleaned, "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err = os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrFolderDenylistReviewRequired
		}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Dev == 0 || stat.Ino == 0 {
		return "", ErrFolderDenylistReviewRequired
	}
	return fmt.Sprintf("dev:%d:ino:%d", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func (g *Guard) markFolderDenyNeedsReview(ctx context.Context, tokenID, spaceID, mountID, relativePath string) error {
	if g == nil || g.db == nil {
		return errors.Join(
			ErrFolderDenylistReviewRequired,
			errors.New("persist folder deny needs_review: access guard is unavailable"),
		)
	}
	result, err := g.db.ExecContext(ctx, `
UPDATE ai_token_folder_denies
SET status = 'needs_review', updated_at = CURRENT_TIMESTAMP
WHERE token_id = ? AND space_id = ? AND mount_id = ? AND relative_path = ?
`, tokenID, spaceID, mountID, relativePath)
	if err != nil {
		return errors.Join(
			ErrFolderDenylistReviewRequired,
			fmt.Errorf("persist folder deny needs_review: %w", err),
		)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return errors.Join(
			ErrFolderDenylistReviewRequired,
			fmt.Errorf("read folder deny needs_review affected rows: %w", err),
		)
	}
	if affected != 1 {
		return errors.Join(
			ErrFolderDenylistReviewRequired,
			fmt.Errorf("persist folder deny needs_review: affected %d rows, want 1", affected),
		)
	}
	return nil
}
