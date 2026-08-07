package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"

	"omnora/internal/access"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
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
	result, err := s.memberFiles.RenameSecure(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{
		SpaceID: spaceID, MountID: mountID, Path: req.From,
	}, target, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "object_rename", "file_object", target, `{}`)
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "renaming requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
}

func (s *Server) moveObject(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
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
	result, err := s.memberFiles.MoveSecure(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{
		SpaceID: spaceID, MountID: mountID, Path: req.From,
	}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: req.ToDir}, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "object_move", "file_object", req.From, `{}`)
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "moving requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	relativePath := r.URL.Query().Get("path")
	if strings.TrimSpace(relativePath) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "path is required")
		return
	}
	permanent := r.URL.Query().Get("permanent") == "1" || strings.EqualFold(r.URL.Query().Get("permanent"), "true")
	if !permanent {
		item, err := s.memberFiles.TrashSecure(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: relativePath}, func(ctx context.Context, tx *sql.Tx) error {
			return s.recordAuditTx(ctx, tx, r, session.AccountID, "object_trash", "file_object", relativePath, `{}`)
		})
		if err != nil {
			if errors.Is(err, files.ErrNotManagedMount) {
				httpx.WriteError(w, r, http.StatusBadRequest, "confirm_permanent_delete", "external mounts require permanent=true after explicit confirmation")
				return
			}
			writeMemberFilesError(w, r, err, "deleting requires editor permission")
			return
		}
		// Keep the legacy trash-item response shape while the shared service
		// owns authorization, mutation, and share invalidation.
		legacy := map[string]any{
			"id": item.TrashID, "originalPath": item.OriginalPath, "name": item.Name,
			"kind": item.Kind, "size": item.Size, "deletedAt": item.DeletedAt,
			"trashRelativePath": item.TrashRelativePath,
		}
		httpx.WriteJSON(w, http.StatusOK, legacy)
		return
	}
	result, err := s.memberFiles.DeletePermanentlySecure(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: relativePath}, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "object_delete", "file_object", relativePath, `{}`)
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "deleting requires editor permission")
		return
	}
	_ = result
	w.WriteHeader(http.StatusNoContent)
	return
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	result, err := s.memberFiles.ListTrashViewer(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: "."})
	if err != nil {
		writeMemberFilesError(w, r, err, "listing trash requires viewer permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": result.Items})
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
	result, err := s.memberFiles.RestoreTrashSecure(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: "."}, trashID, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "object_trash_restore", "file_object", trashID, fmt.Sprintf(`{"trashId":%q}`, trashID))
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "restoring trash requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
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
	if _, err := s.memberFiles.PurgeTrash(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: "."}, trashID); err != nil {
		writeMemberFilesError(w, r, err, "purging trash requires editor permission")
		return
	}
	if !s.recordAuditMutation(w, r, "object_trash_purge", "file_object", trashID, `{}`) {
		return
	}
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
	result, err := s.memberFiles.EmptyTrash(r.Context(), access.Subject{AccountID: session.AccountID}, access.Locator{SpaceID: spaceID, MountID: mountID, Path: "."})
	if err != nil {
		writeMemberFilesError(w, r, err, "emptying trash requires editor permission")
		return
	}
	if !s.recordAuditMutation(w, r, "object_trash_empty", "file_object", mountID, fmt.Sprintf(`{"removed":%d}`, result.AffectedCount)) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"removed": result.AffectedCount})
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
	subject := access.Subject{AccountID: session.AccountID}
	var mutation memberfiles.MutationResult
	if move {
		mutation, err = s.memberFiles.CrossMountMove(r.Context(), subject,
			access.Locator{SpaceID: sourceSpaceID, MountID: sourceMountID, Path: req.From},
			access.Locator{SpaceID: req.ToSpaceID, MountID: req.ToMountID, Path: req.ToDir})
	} else {
		mutation, err = s.memberFiles.CrossMountCopy(r.Context(), subject,
			access.Locator{SpaceID: sourceSpaceID, MountID: sourceMountID, Path: req.From},
			access.Locator{SpaceID: req.ToSpaceID, MountID: req.ToMountID, Path: req.ToDir})
	}
	if err != nil {
		if errors.Is(err, memberfiles.ErrCrossMountSameMount) {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "source and destination mounts must differ")
			return
		}
		if errors.Is(err, files.ErrCrossMountIncomplete) {
			if !s.recordAuditMutation(w, r, "object_cross_mount_incomplete", "file_object", mutation.RelativePath, fmt.Sprintf(`{"error":%q}`, err.Error())) {
				return
			}
			httpx.WriteError(w, r, http.StatusConflict, "cross_mount_incomplete", err.Error())
			return
		}
		writeMemberFilesError(w, r, err, "cross-mount operation requires editor permission")
		return
	}
	action := "object_cross_mount_copy"
	if move {
		action = "object_cross_mount_move"
	}
	if !s.recordAuditMutation(w, r, action, "file_object", mutation.RelativePath, fmt.Sprintf(`{"fromMount":%q,"toMount":%q}`, sourceMountID, req.ToMountID)) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"relativePath": mutation.RelativePath,
		"spaceId":      req.ToSpaceID,
		"mountId":      req.ToMountID,
	})
}

func mountForListingFromAuthorized(mount access.AuthorizedMount) mountForListing {
	return mountForListing{
		ID:           mount.ID,
		Root:         mount.Root,
		Mode:         mount.Mode,
		Kind:         mount.Kind,
		IdentityJSON: mount.IdentityJSON,
	}
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

// writeMemberFilesError translates shared application errors to the stable
// REST envelope. The service owns authorization and filesystem policy; this
// adapter owns only HTTP status/code/message compatibility.
func writeMemberFilesError(w http.ResponseWriter, r *http.Request, err error, forbiddenMessage string) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, memberfiles.ErrCrossMountSameMount):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "source and destination mounts must differ")
	case errors.Is(err, memberfiles.ErrUploadNotFound), errors.Is(err, memberfiles.ErrUploadExpired):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "upload session was not found")
	case errors.Is(err, files.ErrTrashItemNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "trash item was not found")
	case errors.Is(err, os.ErrNotExist):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "file or directory was not found")
	case errors.Is(err, sql.ErrNoRows):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
	case errors.Is(err, files.ErrInvalidMount):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
	case errors.Is(err, memberfiles.ErrMutationInvalidPath):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "path is invalid")
	case errors.Is(err, memberfiles.ErrUploadConflict), errors.Is(err, memberfiles.ErrMutationConflict):
		code := "upload_conflict"
		message := "upload could not be completed"
		if errors.Is(err, memberfiles.ErrMutationConflict) {
			code = "conflict"
			message = "file operation conflicts with the current object"
		}
		httpx.WriteError(w, r, http.StatusConflict, code, message)
	case errors.Is(err, access.ErrMountIdentityUnverifiable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
	case errors.Is(err, access.ErrMountUnavailable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_unavailable", "mount is unavailable and must be re-verified by an administrator")
	case errors.Is(err, access.ErrReadonlyMount), errors.Is(err, files.ErrInvalidMountMode):
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
	case errors.Is(err, access.ErrUnauthorized):
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
	case errors.Is(err, access.ErrForbidden):
		if forbiddenMessage == "" {
			forbiddenMessage = "insufficient space permission"
		}
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", forbiddenMessage)
	case errors.Is(err, access.ErrBoundaryViolation), errors.Is(err, memberfiles.ErrInvalidInput), errors.Is(err, files.ErrNotFile), errors.Is(err, files.ErrNotDirectory), errors.Is(err, files.ErrSymlinkPath):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "path is invalid")
	case errors.Is(err, files.ErrNotManagedMount):
		httpx.WriteError(w, r, http.StatusBadRequest, "not_managed_mount", "operation requires a managed mount")
	default:
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "target already exists") {
			httpx.WriteError(w, r, http.StatusConflict, "conflict", "target already exists")
			return
		}
		if strings.Contains(lower, "path is invalid") || strings.Contains(lower, "paths are not allowed") || strings.Contains(lower, "path traversal") || strings.Contains(lower, "reserved namespace") {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "path is invalid")
			return
		}
		if isReadOnlyFilesystem(err) {
			httpx.WriteError(w, r, http.StatusConflict, "mount_not_writable", "mount root is not writable at the container filesystem layer")
			return
		}
		writeDBError(w, r, err)
	}
}
