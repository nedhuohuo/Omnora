package memberfiles

import (
	"context"
	"path"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/fileops"
	"omnora/internal/files"
)

var ErrTrashTooLarge = files.ErrTrashTooLarge

// Trash uses the deleting account's protected personal trash. Common-mount
// cross-root trash publication is rejected until it can be completed without
// risking source loss; callers may then offer the confirmed permanent-delete
// flow for oversized or otherwise non-trashable content.
func (s *Service) Trash(ctx context.Context, subject access.Subject, locator access.Locator) (TrashResult, error) {
	mount, err := s.authorizeTrashSource(ctx, subject, locator)
	if err != nil {
		return TrashResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(mount), mount.RelativePath)
	var item files.TrashItem
	if mount.Source == contentref.SourcePersonal {
		item, err = s.files.SoftDelete(toFilesMount(mount), mount.RelativePath)
	} else {
		personal, personalErr := s.guard.Authorize(ctx, access.CheckRequest{
			Subject: access.Subject{AccountID: subject.AccountID}, Scope: aitoken.ScopeFilesTrash,
			Locator:            access.Locator{Source: contentref.SourcePersonal, Path: "."},
			RequiredPermission: domain.ContentPermissionEditor, Write: true,
		})
		if personalErr != nil {
			return TrashResult{}, personalErr
		}
		item, err = s.files.SoftDeleteToPersonalTrash(toFilesMount(mount), toFilesMount(personal), mount.RelativePath)
	}
	if err != nil {
		return TrashResult{}, err
	}
	if err := s.invalidatePath(ctx, mount.ID, mount.StorageRelativePath); err != nil {
		return TrashResult{}, err
	}
	return trashResultFrom(item, entry), nil
}

func (s *Service) TrashSecure(ctx context.Context, subject access.Subject, locator access.Locator, auditWriter fileops.AuditWriter) (TrashResult, error) {
	if s.fileOps == nil {
		return s.Trash(ctx, subject, locator)
	}
	source, err := s.authorizeTrashSource(ctx, subject, locator)
	if err != nil {
		return TrashResult{}, err
	}
	personal, err := s.guard.Authorize(ctx, access.CheckRequest{
		Subject: access.Subject{AccountID: subject.AccountID}, Scope: aitoken.ScopeFilesTrash,
		Locator:            access.Locator{Source: contentref.SourcePersonal, Path: "."},
		RequiredPermission: domain.ContentPermissionEditor, Write: true,
	})
	if err != nil {
		return TrashResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(source), source.RelativePath)
	item, err := s.fileOps.TrashToPersonal(ctx, toFilesMount(source), toFilesMount(personal),
		source.ID, personal.ID, source.StorageRelativePath, personal.StorageRelativePath, source.RelativePath, auditWriter)
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
	return s.listTrashWithPermission(ctx, subject, locator, aitoken.ScopeTrashRead, domain.ContentPermissionEditor, false)
}

func (s *Service) PreviewTrash(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (TrashListResult, error) {
	return s.listTrashWithPermission(ctx, subject, locator, scope, domain.ContentPermissionEditor, true)
}

func (s *Service) PreviewEmptyTrash(ctx context.Context, subject access.Subject, locator access.Locator) (TrashListResult, error) {
	result, err := s.PreviewTrash(ctx, subject, locator, aitoken.ScopeFilesPurge)
	if err != nil {
		return TrashListResult{}, err
	}
	if subject.Principal != nil && !hasRootBoundary(subject.Principal, contentref.SourcePersonal, "") {
		return TrashListResult{}, access.ErrForbidden
	}
	return result, nil
}

func (s *Service) ListTrashViewer(ctx context.Context, subject access.Subject, locator access.Locator) (TrashListResult, error) {
	return s.listTrashWithPermission(ctx, subject, locator, aitoken.ScopeTrashRead, domain.ContentPermissionViewer, false)
}

func (s *Service) listTrashWithPermission(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope, permission domain.ContentPermission, write bool) (TrashListResult, error) {
	mount, fresh, err := s.authorizePersonalTrashRoot(ctx, subject, locator, scope, permission, write)
	if err != nil {
		return TrashListResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil {
		return TrashListResult{}, err
	}
	if fresh.Principal != nil {
		items = filterTrashItems(fresh.Principal, contentref.SourcePersonal, "", items)
	}
	return summarizeTrash(items), nil
}

func (s *Service) RestoreTrash(ctx context.Context, subject access.Subject, locator access.Locator, trashID string) (MutationResult, error) {
	mount, fresh, err := s.authorizePersonalTrashRoot(ctx, subject, locator, aitoken.ScopeFilesRestore, domain.ContentPermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil || !trashIDVisible(fresh.Principal, items, trashID) {
		return MutationResult{}, access.ErrForbidden
	}
	restored, err := s.files.RestoreTrash(toFilesMount(mount), trashID)
	if err != nil {
		return MutationResult{}, err
	}
	return s.restoreResult(mount, restored), nil
}

func (s *Service) RestoreTrashSecure(ctx context.Context, subject access.Subject, locator access.Locator, trashID string, auditWriter fileops.AuditWriter) (MutationResult, error) {
	return s.RestoreTrash(ctx, subject, locator, trashID)
}

func (s *Service) restoreResult(mount access.AuthorizedMount, restored string) MutationResult {
	result := MutationResult{RelativePath: path.Clean(restored)}
	if entry, err := s.files.Stat(toFilesMount(mount), result.RelativePath); err == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
		result.TotalBytes = entry.Size
	}
	return result
}

func (s *Service) PurgeTrash(ctx context.Context, subject access.Subject, locator access.Locator, trashID string) (MutationResult, error) {
	mount, fresh, err := s.authorizePersonalTrashRoot(ctx, subject, locator, aitoken.ScopeFilesPurge, domain.ContentPermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil || !trashIDVisible(fresh.Principal, items, trashID) {
		return MutationResult{}, access.ErrForbidden
	}
	var size int64
	for _, item := range items {
		if item.ID == trashID {
			size = item.Size
		}
	}
	if err := s.files.PurgeTrash(toFilesMount(mount), trashID); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: trashID, AffectedCount: 1, TotalBytes: size}, nil
}

func (s *Service) PurgeTrashSecure(ctx context.Context, subject access.Subject, locator access.Locator, trashID string, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.PurgeTrash(ctx, subject, locator, trashID)
	}
	mount, fresh, err := s.authorizePersonalTrashRoot(ctx, subject, locator, aitoken.ScopeFilesPurge, domain.ContentPermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil || !trashIDVisible(fresh.Principal, items, trashID) {
		return MutationResult{}, access.ErrForbidden
	}
	var size int64
	for _, item := range items {
		if item.ID == trashID {
			size = item.Size
			break
		}
	}
	storagePath := path.Join(mount.StorageRelativePath, ".omnora", "trash", trashID)
	if err := s.fileOps.PurgeTrash(ctx, toFilesMount(mount), mount.ID, storagePath, trashID, auditWriter); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: trashID, AffectedCount: 1, TotalBytes: size}, nil
}

func (s *Service) EmptyTrash(ctx context.Context, subject access.Subject, locator access.Locator) (MutationResult, error) {
	mount, fresh, err := s.authorizePersonalTrashRoot(ctx, subject, locator, aitoken.ScopeFilesPurge, domain.ContentPermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	if fresh.Principal != nil && !hasRootBoundary(fresh.Principal, contentref.SourcePersonal, "") {
		return MutationResult{}, access.ErrForbidden
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil {
		return MutationResult{}, err
	}
	summary := summarizeTrash(items)
	removed, err := s.files.EmptyTrash(toFilesMount(mount))
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{AffectedCount: removed, TotalBytes: summary.TotalBytes}, nil
}

func (s *Service) EmptyTrashSecure(ctx context.Context, subject access.Subject, locator access.Locator, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.EmptyTrash(ctx, subject, locator)
	}
	mount, fresh, err := s.authorizePersonalTrashRoot(ctx, subject, locator, aitoken.ScopeFilesPurge, domain.ContentPermissionEditor, true)
	if err != nil {
		return MutationResult{}, err
	}
	if fresh.Principal != nil && !hasRootBoundary(fresh.Principal, contentref.SourcePersonal, "") {
		return MutationResult{}, access.ErrForbidden
	}
	items, err := s.files.ListTrash(toFilesMount(mount))
	if err != nil {
		return MutationResult{}, err
	}
	summary := summarizeTrash(items)
	storagePath := path.Join(mount.StorageRelativePath, ".omnora", "trash")
	removed, err := s.fileOps.EmptyTrash(ctx, toFilesMount(mount), mount.ID, storagePath, auditWriter)
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
	if err := s.invalidatePath(ctx, mount.ID, mount.StorageRelativePath); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: mount.RelativePath, ObjectFingerprint: entry.ObjectFingerprint, AffectedCount: 1, TotalBytes: entry.Size}, nil
}

func (s *Service) DeletePermanentlySecure(ctx context.Context, subject access.Subject, locator access.Locator, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.DeletePermanently(ctx, subject, locator)
	}
	mount, err := s.authorizeMutationSource(ctx, subject, locator, aitoken.ScopeFilesPurge)
	if err != nil {
		return MutationResult{}, err
	}
	entry, _ := s.files.Stat(toFilesMount(mount), mount.RelativePath)
	if err := s.fileOps.Delete(ctx, toStorageFilesMount(mount), mount.ID, mount.StorageRelativePath, auditWriter); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: mount.RelativePath, ObjectFingerprint: entry.ObjectFingerprint, AffectedCount: 1, TotalBytes: entry.Size}, nil
}

func (s *Service) authorizePersonalTrashRoot(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope, permission domain.ContentPermission, write bool) (access.AuthorizedMount, access.Subject, error) {
	if locator.Source != contentref.SourcePersonal || locator.MountID != "" {
		return access.AuthorizedMount{}, subject, files.ErrNotManagedMount
	}
	fresh, err := s.requireTokenScope(ctx, subject, scope)
	if err != nil {
		return access.AuthorizedMount{}, subject, err
	}
	paths := currentBoundaryPaths(fresh.Principal, contentref.SourcePersonal, "")
	for _, boundary := range paths {
		mount, authErr := s.guard.Authorize(ctx, access.CheckRequest{
			Subject: fresh, Scope: scope,
			Locator:            access.Locator{Source: contentref.SourcePersonal, Path: boundary},
			RequiredPermission: permission, Write: write,
		})
		if authErr == nil {
			mount.RelativePath = "."
			mount.StorageRelativePath = subject.AccountID
			return mount, fresh, nil
		}
	}
	return access.AuthorizedMount{}, fresh, access.ErrForbidden
}

func filterTrashItems(principal *aitoken.Principal, source contentref.Source, mountID string, items []files.TrashItem) []files.TrashItem {
	if principal == nil {
		return items
	}
	paths := currentBoundaryPaths(principal, source, mountID)
	allowed := map[string][]string{mountID: paths}
	filtered := make([]files.TrashItem, 0, len(items))
	for _, item := range items {
		if pathInBoundaries(mountID, item.OriginalPath, allowed) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func trashIDVisible(principal *aitoken.Principal, items []files.TrashItem, id string) bool {
	for _, item := range filterTrashItems(principal, contentref.SourcePersonal, "", items) {
		if item.ID == id {
			return true
		}
	}
	return false
}

func hasRootBoundary(principal *aitoken.Principal, source contentref.Source, mountID string) bool {
	for _, value := range currentBoundaryPaths(principal, source, mountID) {
		if value == "." || value == "" {
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

func toStorageFilesMount(mount access.AuthorizedMount) files.Mount {
	kind := "external"
	if mount.StorageKind == domain.StorageKindManaged {
		kind = "managed"
	}
	return files.Mount{Root: mount.MountRoot, Mode: mount.Mode, Kind: kind}
}
