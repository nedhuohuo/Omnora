package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"omnora/internal/httpx"
	updatepkg "omnora/internal/update"
)

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.updates == nil || !s.updates.Enabled() {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "update_disabled", "self-update is not available in this deployment")
		return
	}
	status, err := s.updates.Status()
	if err != nil {
		slog.ErrorContext(r.Context(), "read self-update status", "error", err)
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "update_status_unavailable", "self-update status is unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, status)
}

func (s *Server) uploadUpdate(w http.ResponseWriter, r *http.Request) {
	_, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.updates == nil || !s.updates.Enabled() {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "update_disabled", "self-update is not available in this deployment")
		return
	}
	maxBytes := s.updates.MaxPackageBytes()
	if r.ContentLength > maxBytes+(1<<20) {
		writeUpdatePackageError(w, r, fmt.Errorf("package exceeds configured upload limit"), http.StatusRequestEntityTooLarge)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		writeUpdatePackageError(w, r, fmt.Errorf("request must be multipart/form-data"), http.StatusBadRequest)
		return
	}
	// Leave a small allowance for multipart headers and the field boundary while
	// keeping the archive itself bounded by Manager.StageReader.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	multipartReader, err := r.MultipartReader()
	if err != nil {
		writeUpdatePackageError(w, r, err, http.StatusBadRequest)
		return
	}
	var release updatepkg.Release
	found := false
	for {
		part, nextErr := multipartReader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			writeUpdatePackageError(w, r, nextErr, http.StatusBadRequest)
			return
		}
		if part.FormName() != "package" || found {
			_ = part.Close()
			writeUpdatePackageError(w, r, fmt.Errorf("exactly one package field is required"), http.StatusBadRequest)
			return
		}
		found = true
		release, err = s.updates.StageReader(r.Context(), part)
		_ = part.Close()
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, updatepkg.ErrPendingUpdate) {
				status = http.StatusConflict
			}
			writeUpdatePackageError(w, r, err, status)
			return
		}
	}
	if !found {
		writeUpdatePackageError(w, r, fmt.Errorf("package field is required"), http.StatusBadRequest)
		return
	}
	if err := s.updates.Activate(release); err != nil {
		if cancelErr := s.updates.CancelPending(); cancelErr != nil {
			slog.ErrorContext(r.Context(), "cancel failed self-update", "error", cancelErr)
		}
		writeUpdatePackageError(w, r, err, http.StatusConflict)
		return
	}
	if !s.recordAuditMutation(w, r, "admin_update_stage", "update", release.ID, fmt.Sprintf(`{"version":%q,"archive_sha256":%q}`, release.Version, release.ArchiveSHA256)) {
		_ = s.updates.CancelPending()
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"state":   "pending_restart",
		"release": release,
		"message": "update package verified and queued; the service will restart and activate it",
	})
	// The response is written before this signal. The entrypoint owns the
	// restart and retains the previous release for automatic rollback.
	s.RequestShutdown()
}

func (s *Server) rollbackUpdate(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if s.updates == nil || !s.updates.Enabled() {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "update_disabled", "self-update is not available in this deployment")
		return
	}
	if err := s.updates.RequestRollback(); err != nil {
		status := http.StatusConflict
		if errors.Is(err, updatepkg.ErrDisabled) {
			status = http.StatusServiceUnavailable
		}
		httpx.WriteError(w, r, status, "rollback_unavailable", "there is no installed update available for rollback")
		return
	}
	if !s.recordAuditMutation(w, r, "admin_update_rollback", "update", "active", fmt.Sprintf(`{"by":%q}`, session.AccountID)) {
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"state":   "rollback_pending",
		"message": "rollback queued; the service will restart and restore the previous release",
	})
	s.RequestShutdown()
}

func writeUpdatePackageError(w http.ResponseWriter, r *http.Request, err error, status int) {
	// Archive errors can contain local paths or parser details. Keep them in
	// structured logs and expose a stable generic error to the browser.
	slog.WarnContext(r.Context(), "self-update package rejected", "error", err)
	code := "invalid_update_package"
	message := "the update package could not be accepted"
	if errors.Is(err, updatepkg.ErrUnsupportedTarget) {
		code = "unsupported_update_target"
		message = "the update package targets a different platform"
	}
	if errors.Is(err, updatepkg.ErrDisabled) {
		code = "update_disabled"
		message = "self-update is not available in this deployment"
	}
	httpx.WriteError(w, r, status, code, strings.TrimSpace(message))
}
