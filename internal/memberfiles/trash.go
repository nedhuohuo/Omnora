package memberfiles

import (
	"context"
	"errors"
	"path"
	"strings"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/fileops"
)

func (s *Service) Trash(ctx context.Context, subject access.Subject, locator access.Locator) (TrashResult, error) {
	mount, err := s.authorizeTrashSource(ctx, subject, locator)
	if err != nil {
		return TrashResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(mount), mount.RelativePath)
	item, err := s.files.SoftDelete(toFilesMount(mount), mount.RelativePath)
	if err != nil {
		return TrashResult{}, err
	}
	if err := s.invalidatePath(ctx, locator, mount.RelativePath); err != nil {
		return TrashResult{}, err
	}
	return trashResultFrom(item, entry), nil
}

// TrashSecure behaves like Trash but, when a fileops.Coordinator is
// configured, durably journals the soft delete, invalidates affected shares,
// and records auditWriter's event all in one transaction before any
// filesystem I/O begins.
func (s *Service) TrashSecure(ctx context.Context, subject access.Subject, locator access.Locator, auditWriter fileops.AuditWriter) (TrashResult, error) {
	if s.fileOps == nil {
		return s.Trash(ctx, subject, locator)
	}
	mount, err := s.authorizeTrashSource(ctx, subject, locator)
	if err != nil {
		return TrashResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(mount), mount.RelativePath)
	item, err := s.fileOps.Trash(ctx, toFilesMount(mount), locator.SpaceID, locator.MountID, mount.RelativePath, auditWriter)
	if err != nil {
		return TrashResult{}, err
	}
	return trashResultFrom(item, entry), nil
}

func (s *Service) authorizeTrashSource(ctx context.Context, subject access.Subject, locator access.Locator) (access.AuthorizedMount, error) {
	mount, err := s.authorizeMutationSource(ctx, subject, locator, aitoken.ScopeFilesTrash)
	if err != nil {
		return access.AuthorizedMount{}, err
	}
	if mount.Kind != "managed" {
		return access.AuthorizedMount{}, files.ErrNotManagedMount
	}
	return mount, nil
}

func trashResultFrom(item files.TrashItem, entry files.Entry) TrashResult {
	return TrashResult{
		TrashID: item.ID, OriginalPath: item.OriginalPath, Name: item.Name, Kind: item.Kind,
		DeletedAt: item.DeletedAt, TrashRelativePath: item.TrashRelativePath,
		ObjectFingerprint: entry.ObjectFingerprint, Size: item.Size,
	}
}

func (s *Service) ListTrash(ctx context.Context, subject access.Subject, locator access.Locator) (TrashListResult, error) {
	return s.listTrashWithPermission(ctx, subject, locator, aitoken.ScopeTrashRead, domain.SpacePermissionEditor, false)
}

// PreviewTrash performs a live authorization check under the operation scope
// used by a destructive caller, allowing confirmation previews without
// requiring an unrelated trash:read token scope.
func (s *Service) PreviewTrash(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (TrashListResult, error) {
	return s.listTrashWithPermission(ctx, subject, locator, scope, domain.SpacePermissionEditor, true)
}

// PreviewEmptyTrash performs the purge-scope preview plus the root-boundary
// invariant required by EmptyTrash. A narrower boundary may purge visible
// items individually but cannot empty the entire managed trash.
func (s *Service) PreviewEmptyTrash(ctx context.Context, subject access.Subject, locator access.Locator) (TrashListResult, error) {
	result, err := s.PreviewTrash(ctx, subject, locator, aitoken.ScopeFilesPurge)
	if err != nil {
		return TrashListResult{}, err
	}
	if subject.Principal != nil && !s.hasRootBoundary(ctx, subject, locator.SpaceID, locator.MountID) {
		return TrashListResult{}, access.ErrForbidden
	}
	return result, nil
}

// ListTrashViewer is the compatibility entry point for the existing REST GET
// route, which historically allowed viewer ACLs. MCP's default ListTrash
// remains Editor+ as required by the tool contract.
func (s *Service) ListTrashViewer(ctx context.Context, subject access.Subject, locator access.Locator) (TrashListResult, error) {
	return s.listTrashWithPermission(ctx, subject, locator, aitoken.ScopeTrashRead, domain.SpacePermissionViewer, false)
}

func (s *Service) listTrashWithPermission(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope, permission domain.SpacePermission, write bool) (TrashListResult, error) {
	mount, err := s.authorizeTrashMount(ctx, subject, locator, scope, permission, write)
	if err != nil {
		return TrashListResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil {
		return TrashListResult{}, err
	}
	if subject.Principal != nil {
		items = s.filterTrashItems(ctx, subject, mount.SpaceID, mount.ID, items)
	}
	return summarizeTrash(items), nil
}

func (s *Service) RestoreTrash(ctx context.Context, subject access.Subject, locator access.Locator, trashID string) (MutationResult, error) {
	mount, err := s.authorizeRestoreSource(ctx, subject, locator, trashID)
	if err != nil {
		return MutationResult{}, err
	}
	restored, err := s.files.RestoreTrash(toFilesMount(mount), trashID)
	if err != nil {
		return MutationResult{}, err
	}
	return s.restoreResult(mount, restored), nil
}

// RestoreTrashSecure behaves like RestoreTrash but, when a
// fileops.Coordinator is configured, durably journals the restore and
// records auditWriter's event in the same transaction before any filesystem
// I/O begins.
func (s *Service) RestoreTrashSecure(ctx context.Context, subject access.Subject, locator access.Locator, trashID string, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.RestoreTrash(ctx, subject, locator, trashID)
	}
	mount, err := s.authorizeRestoreSource(ctx, subject, locator, trashID)
	if err != nil {
		return MutationResult{}, err
	}
	restored, err := s.fileOps.RestoreTrash(ctx, toFilesMount(mount), locator.SpaceID, locator.MountID, trashID, auditWriter)
	if err != nil {
		return MutationResult{}, err
	}
	return s.restoreResult(mount, restored), nil
}

func (s *Service) authorizeRestoreSource(ctx context.Context, subject access.Subject, locator access.Locator, trashID string) (access.AuthorizedMount, error) {
	mount, err := s.authorizeTrashMount(ctx, subject, locator, aitoken.ScopeFilesRestore, domain.SpacePermissionEditor, true)
	if err != nil {
		return access.AuthorizedMount{}, err
	}
	if !s.trashIDVisible(ctx, subject, mount.SpaceID, mount.ID, toFilesMount(mount), trashID) {
		return access.AuthorizedMount{}, access.ErrForbidden
	}
	return mount, nil
}

func (s *Service) restoreResult(mount access.AuthorizedMount, restored string) MutationResult {
	result := MutationResult{RelativePath: path.Clean(restored)}
	if entry, statErr := s.files.Stat(toFilesMount(mount), result.RelativePath); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
		result.TotalBytes = entry.Size
	}
	return result
}

func (s *Service) PurgeTrash(ctx context.Context, subject access.Subject, locator access.Locator, trashID string) (MutationResult, error) {
	mount, err := s.authorizeTrashMount(ctx, subject, locator, aitoken.ScopeFilesPurge, domain.SpacePermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil {
		return MutationResult{}, err
	}
	var bytes int64
	if subject.Principal != nil && !s.trashIDVisibleFromItems(ctx, subject, mount.SpaceID, mount.ID, items, trashID) {
		return MutationResult{}, access.ErrForbidden
	}
	for _, item := range items {
		if item.ID == trashID {
			bytes = item.Size
			break
		}
	}
	if err := s.files.PurgeTrash(toFilesMount(mount), trashID); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: trashID, AffectedCount: 1, TotalBytes: bytes}, nil
}

func (s *Service) EmptyTrash(ctx context.Context, subject access.Subject, locator access.Locator) (MutationResult, error) {
	mount, err := s.authorizeTrashMount(ctx, subject, locator, aitoken.ScopeFilesPurge, domain.SpacePermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil {
		return MutationResult{}, err
	}
	if subject.Principal != nil && !s.hasRootBoundary(ctx, subject, mount.SpaceID, mount.ID) {
		return MutationResult{}, access.ErrForbidden
	}
	summary := summarizeTrash(items)
	removed, err := s.files.EmptyTrash(toFilesMount(mount))
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{AffectedCount: removed, TotalBytes: summary.TotalBytes}, nil
}

func (s *Service) DeletePermanently(ctx context.Context, subject access.Subject, locator access.Locator) (MutationResult, error) {
	mount, err := s.authorizeMutationSource(ctx, subject, locator, aitoken.ScopeFilesPurge)
	if err != nil {
		return MutationResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(mount), mount.RelativePath)
	if err := s.files.Delete(toFilesMount(mount), mount.RelativePath); err != nil {
		return MutationResult{}, err
	}
	if err := s.invalidatePath(ctx, locator, mount.RelativePath); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: mount.RelativePath, ObjectFingerprint: entry.ObjectFingerprint, AffectedCount: 1, TotalBytes: entry.Size}, nil
}

// DeletePermanentlySecure behaves like DeletePermanently but, when a
// fileops.Coordinator is configured, durably journals the delete,
// invalidates affected shares, and records auditWriter's event all in one
// transaction before any filesystem I/O begins.
func (s *Service) DeletePermanentlySecure(ctx context.Context, subject access.Subject, locator access.Locator, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.DeletePermanently(ctx, subject, locator)
	}
	mount, err := s.authorizeMutationSource(ctx, subject, locator, aitoken.ScopeFilesPurge)
	if err != nil {
		return MutationResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(mount), mount.RelativePath)
	if err := s.fileOps.Delete(ctx, toFilesMount(mount), locator.SpaceID, locator.MountID, mount.RelativePath, auditWriter); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: mount.RelativePath, ObjectFingerprint: entry.ObjectFingerprint, AffectedCount: 1, TotalBytes: entry.Size}, nil
}

func (s *Service) authorizeTrashMount(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope, permission domain.SpacePermission, write bool) (access.AuthorizedMount, error) {
	if locator.Path == "" {
		locator.Path = "."
	}
	mount, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: permission, Write: write})
	fromBoundary := false
	if err != nil && subject.Principal != nil {
		// A token boundary may not include the mount root itself. Re-authorize
		// against one current boundary to validate the mount, then filter trash
		// metadata by those same boundaries before returning it.
		boundaries, boundaryErr := s.currentBoundaries(ctx, subject, locator.SpaceID, locator.MountID)
		if boundaryErr == nil {
			for _, boundary := range boundaries {
				candidate, candidateErr := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope,
					Locator:            access.Locator{SpaceID: locator.SpaceID, MountID: locator.MountID, Path: boundary},
					RequiredPermission: permission, Write: write})
				if candidateErr == nil {
					mount, err = candidate, nil
					fromBoundary = true
					break
				}
			}
		}
	}
	if err != nil {
		return access.AuthorizedMount{}, err
	}
	if mount.Kind != "managed" {
		return access.AuthorizedMount{}, files.ErrNotManagedMount
	}
	if mount.RelativePath != "." && !fromBoundary {
		return access.AuthorizedMount{}, errors.New("trash operations require mount root")
	}
	if fromBoundary {
		mount.RelativePath = "."
	}
	return mount, nil
}

func (s *Service) filterTrashItems(ctx context.Context, subject access.Subject, spaceID, mountID string, items []files.TrashItem) []files.TrashItem {
	boundaries, err := s.currentBoundaries(ctx, subject, spaceID, mountID)
	if err != nil {
		return nil
	}
	allowed := make(map[string][]string, 1)
	for index, boundary := range boundaries {
		if boundary == "." {
			boundaries[index] = ""
		}
	}
	allowed[mountID] = boundaries
	filtered := make([]files.TrashItem, 0, len(items))
	for _, item := range items {
		if pathInBoundaries(mountID, item.OriginalPath, allowed) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *Service) trashIDVisible(ctx context.Context, subject access.Subject, spaceID, mountID string, mount files.Mount, id string) bool {
	items, err := s.files.ListTrash(mount)
	if err != nil {
		return false
	}
	return s.trashIDVisibleFromItems(ctx, subject, spaceID, mountID, items, id)
}

func (s *Service) trashIDVisibleFromItems(ctx context.Context, subject access.Subject, spaceID, mountID string, items []files.TrashItem, id string) bool {
	if subject.Principal == nil {
		for _, item := range items {
			if item.ID == id {
				return true
			}
		}
		return false
	}
	for _, item := range s.filterTrashItems(ctx, subject, spaceID, mountID, items) {
		if item.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) hasRootBoundary(ctx context.Context, subject access.Subject, spaceID, mountID string) bool {
	boundaries, err := s.currentBoundaries(ctx, subject, spaceID, mountID)
	if err != nil {
		return false
	}
	for _, boundary := range boundaries {
		if strings.TrimSpace(boundary) == "." || strings.TrimSpace(boundary) == "" {
			return true
		}
	}
	return false
}

func summarizeTrash(items []files.TrashItem) TrashListResult {
	result := TrashListResult{Items: items, TotalCount: len(items)}
	for _, item := range items {
		result.TotalBytes += item.Size
	}
	return result
}
