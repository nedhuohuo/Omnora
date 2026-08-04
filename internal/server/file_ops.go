package server

import (
	"errors"
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
	renamed, err := files.NewService().Rename(files.Mount{Root: mount.Root, Mode: mount.Mode}, req.From, target)
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
	moved, err := files.NewService().Move(files.Mount{Root: mount.Root, Mode: mount.Mode}, req.From, req.ToDir)
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
	if err := files.NewService().Delete(files.Mount{Root: mount.Root, Mode: mount.Mode}, relativePath); err != nil {
		writeFileOpError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "object_delete", "file_object", relativePath, `{}`)
	w.WriteHeader(http.StatusNoContent)
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
