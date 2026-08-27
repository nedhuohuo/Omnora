package server

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"

	"omnora/internal/access"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
)

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
			forbiddenMessage = "insufficient content permission"
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
