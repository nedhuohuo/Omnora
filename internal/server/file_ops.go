package server

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/storage"
)

func (s *Server) renameObject(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "renaming requires editor permission")
		return
	}
	var req struct {
		From   string `json:"from"`
		ToName string `json:"toName"`
		ToPath string `json:"toPath"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	target, err := resolveRenameTarget(req.From, req.ToName, req.ToPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	renamed, err := files.NewService().Rename(filesMount(mount), req.From, target)
	if err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "object_rename", "file_object", renamed, `{}`)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": renamed})
}

func (s *Server) moveObject(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "moving requires editor permission")
		return
	}
	var req struct {
		From  string `json:"from"`
		ToDir string `json:"toDir"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.From) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "from is required")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	moved, err := files.NewService().Move(filesMount(mount), req.From, req.ToDir)
	if err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "object_move", "file_object", moved, `{}`)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": moved})
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "deleting requires editor permission")
		return
	}
	relativePath := r.URL.Query().Get("path")
	if strings.TrimSpace(relativePath) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "path is required")
		return
	}
	permanent := r.URL.Query().Get("permanent") == "1" || strings.EqualFold(r.URL.Query().Get("permanent"), "true")
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	svc := files.NewService()
	if mount.Kind == "managed" && !permanent {
		item, err := svc.SoftDelete(filesMount(mount), relativePath)
		if err != nil {
			writeFileOpError(w, r, err)
			return
		}
		_ = s.revokeSharesForPath(r, spaceID, mountID, relativePath)
		_ = s.recordAudit(r, "object_trash", "file_object", relativePath, fmt.Sprintf(`{"trashId":%q}`, item.ID))
		httpx.WriteJSON(w, http.StatusOK, item)
		return
	}
	if mount.Kind != "managed" && !permanent {
		httpx.WriteError(w, r, http.StatusBadRequest, "confirm_permanent_delete", "external mounts require permanent=true after explicit confirmation")
		return
	}
	if err := svc.Delete(filesMount(mount), relativePath); err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.revokeSharesForPath(r, spaceID, mountID, relativePath)
	_ = s.recordAudit(r, "object_delete", "file_object", relativePath, `{}`)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionViewer) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "listing trash requires viewer permission")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	items, err := files.NewService().ListTrash(filesMount(mount))
	if err != nil {
		writeFileOpError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	trashID := r.PathValue("trashId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "restoring trash requires editor permission")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	restored, err := files.NewService().RestoreTrash(filesMount(mount), trashID)
	if err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "object_trash_restore", "file_object", restored, fmt.Sprintf(`{"trashId":%q}`, trashID))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": restored})
}

func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	trashID := r.PathValue("trashId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "purging trash requires editor permission")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	if err := files.NewService().PurgeTrash(filesMount(mount), trashID); err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "object_trash_purge", "file_object", trashID, `{}`)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) emptyTrash(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "emptying trash requires editor permission")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	removed, err := files.NewService().EmptyTrash(filesMount(mount))
	if err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "object_trash_empty", "file_object", mountID, fmt.Sprintf(`{"removed":%d}`, removed))
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"removed": removed})
}

func (s *Server) crossMountCopy(w http.ResponseWriter, r *http.Request) {
	s.handleCrossMount(w, r, false)
}

func (s *Server) crossMountMove(w http.ResponseWriter, r *http.Request) {
	s.handleCrossMount(w, r, true)
}

func (s *Server) handleCrossMount(w http.ResponseWriter, r *http.Request, move bool) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	sourceSpaceID := r.PathValue("spaceId")
	sourceMountID := r.PathValue("mountId")
	var req struct {
		From      string `json:"from"`
		ToSpaceID string `json:"toSpaceId"`
		ToMountID string `json:"toMountId"`
		ToDir     string `json:"toDir"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.ToSpaceID) == "" || strings.TrimSpace(req.ToMountID) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "from, toSpaceId and toMountId are required")
		return
	}
	sourcePerm := domain.SpacePermissionViewer
	if move {
		sourcePerm = domain.SpacePermissionEditor
	}
	if !s.hasSpacePermission(r, session.AccountID, sourceSpaceID, sourcePerm) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "insufficient permission on source space")
		return
	}
	if !s.hasSpacePermission(r, session.AccountID, req.ToSpaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "destination requires editor permission")
		return
	}
	source, err := loadMountForListing(r, s.sqlDB(), sourceSpaceID, sourceMountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	dest, err := loadMountForListing(r, s.sqlDB(), req.ToSpaceID, req.ToMountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if source.ID == dest.ID {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "source and destination mounts must differ")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, source); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "source mount identity could not be verified")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, dest); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "destination mount identity could not be verified")
		return
	}
	svc := files.NewService()
	var result string
	if move {
		result, err = svc.MoveAcrossMounts(filesMount(source), filesMount(dest), req.From, req.ToDir)
	} else {
		result, err = svc.CopyAcrossMounts(filesMount(source), filesMount(dest), req.From, req.ToDir)
	}
	if err != nil {
		if errors.Is(err, files.ErrCrossMountIncomplete) {
			_ = s.recordAudit(r, "object_cross_mount_incomplete", "file_object", result, fmt.Sprintf(`{"error":%q}`, err.Error()))
			httpx.WriteError(w, r, http.StatusConflict, "cross_mount_incomplete", err.Error())
			return
		}
		writeFileOpError(w, r, err)
		return
	}
	action := "object_cross_mount_copy"
	if move {
		action = "object_cross_mount_move"
		_ = s.revokeSharesForPath(r, sourceSpaceID, sourceMountID, req.From)
	}
	_ = s.recordAudit(r, action, "file_object", result, fmt.Sprintf(`{"fromMount":%q,"toMount":%q}`, source.ID, dest.ID))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"relativePath": result,
		"spaceId":      req.ToSpaceID,
		"mountId":      req.ToMountID,
	})
}

func (s *Server) revokeSharesForPath(r *http.Request, spaceID, mountID, relativePath string) error {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil {
		return err
	}
	now := nowRFC3339()
	_, err = s.sqlDB().ExecContext(r.Context(), `
UPDATE shares
SET revoked_at = ?, updated_at = ?
WHERE space_id = ? AND mount_id = ? AND (relative_path = ? OR relative_path LIKE ?) AND revoked_at IS NULL
`, now, now, spaceID, mountID, cleaned, cleaned+"/%")
	return err
}

func filesMount(mount mountForListing) files.Mount {
	return files.Mount{Root: mount.Root, Mode: mount.Mode, Kind: mount.Kind}
}

func resolveRenameTarget(from, toName, toPath string) (string, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return "", errors.New("from is required")
	}
	cleanedFrom, err := storage.CleanRelativePath(from)
	if err != nil {
		return "", errors.New("from is invalid")
	}
	toPath = strings.TrimSpace(toPath)
	if toPath != "" {
		return toPath, nil
	}
	toName = strings.TrimSpace(toName)
	if toName == "" || strings.ContainsAny(toName, "/\\") {
		return "", errors.New("toName or toPath is required")
	}
	parent := path.Dir(cleanedFrom)
	if parent == "." {
		return toName, nil
	}
	return parent + "/" + toName, nil
}

func writeFileOpError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, files.ErrInvalidMountMode):
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
	case errors.Is(err, files.ErrNotManagedMount):
		httpx.WriteError(w, r, http.StatusBadRequest, "not_managed_mount", "operation requires a managed mount")
	case errors.Is(err, files.ErrTrashItemNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "trash item was not found")
	case errors.Is(err, files.ErrSymlinkPath):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "symbolic links are not allowed")
	case errors.Is(err, files.ErrNotFile), errors.Is(err, files.ErrNotDirectory), errors.Is(err, files.ErrInvalidMount):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
	default:
		if isReadOnlyFilesystem(err) {
			httpx.WriteError(w, r, http.StatusConflict, "mount_not_writable", "mount root is not writable at the container filesystem layer")
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "target already exists") {
			httpx.WriteError(w, r, http.StatusConflict, "conflict", err.Error())
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "no such file") {
			httpx.WriteError(w, r, http.StatusNotFound, "not_found", "file or directory was not found")
			return
		}
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
	}
}
