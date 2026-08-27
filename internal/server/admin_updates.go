package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/httpx"
	"omnora/internal/recovery"
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
	session, ok := s.requireAdmin(w, r)
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
			if errors.Is(err, updatepkg.ErrPendingUpdate) || errors.Is(err, updatepkg.ErrUpdateBusy) {
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
	preparation, err := s.updates.BeginPreparation(release)
	if err != nil {
		cleanupStagedRelease(r, s.updates, release)
		writeUpdatePackageError(w, r, err, http.StatusConflict)
		return
	}
	preparationCommitted := false
	defer func() {
		if cancelErr := preparation.Cancel(); cancelErr != nil {
			slog.ErrorContext(r.Context(), "cancel update preparation", "release_id", release.ID, "error", cancelErr)
		}
		if !preparationCommitted {
			cleanupStagedRelease(r, s.updates, release)
		}
	}()
	backupID, err := s.createPreUpdateBackup(r, session.AccountID, release)
	if err != nil {
		slog.ErrorContext(r.Context(), "create pre-update backup", "release_id", release.ID, "error", err)
		httpx.WriteError(w, r, http.StatusInternalServerError, "update_backup_failed", "the update was not queued because its database backup failed")
		return
	}
	release.BackupID = backupID
	if !s.recordAuditMutation(w, r, "admin_update_authorize", "update", release.ID, fmt.Sprintf(`{"version":%q,"archive_sha256":%q,"backup_id":%q,"state":"prepared"}`, release.Version, release.ArchiveSHA256, backupID)) {
		return
	}
	if err := preparation.Commit(release); err != nil {
		writeUpdatePackageError(w, r, err, http.StatusConflict)
		return
	}
	preparationCommitted = true
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
	rollbackToken, err := s.updates.RequestRollbackToken()
	if err != nil {
		status := http.StatusConflict
		code := "rollback_unavailable"
		message := "there is no installed update available for rollback"
		if errors.Is(err, updatepkg.ErrUpdateBusy) {
			code = "update_busy"
			message = "another update operation is already in progress"
		}
		if errors.Is(err, updatepkg.ErrDisabled) {
			status = http.StatusServiceUnavailable
		}
		httpx.WriteError(w, r, status, code, message)
		return
	}
	if !s.recordAuditMutation(w, r, "admin_update_rollback", "update", "active", fmt.Sprintf(`{"by":%q}`, session.AccountID)) {
		if cancelErr := s.updates.CancelRollback(rollbackToken); cancelErr != nil {
			slog.ErrorContext(r.Context(), "cancel unaudited update rollback", "error", cancelErr)
		}
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"state":   "rollback_pending",
		"message": "rollback queued; the service will restart and restore the previous release",
	})
	s.RequestShutdown()
}

func (s *Server) createPreUpdateBackup(r *http.Request, accountID string, release updatepkg.Release) (string, error) {
	if s.db == nil || strings.TrimSpace(s.cfg.Database.Path) == "" {
		return "", errors.New("database is not configured")
	}
	backupsDir := filepath.Join(strings.TrimSpace(s.cfg.Storage.ManagedDir), "backups")
	artifact, err := recovery.NewBackupPublisher(s.db).Publish(r.Context(), backupsDir)
	if err != nil {
		return "", err
	}
	cleanupArtifact := true
	defer func() {
		if cleanupArtifact {
			_ = removeRegularFile(artifact.Path)
		}
	}()
	canonicalPath, err := filepath.Abs(filepath.Clean(artifact.Path))
	if err != nil {
		return "", fmt.Errorf("canonicalize pre-update backup: %w", err)
	}
	backupID := "bkp_" + artifact.ID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	notes := "pre-update backup for release " + release.Version
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO backups(id, status, path, created_by, created_at, completed_at, notes, sha256, size_bytes, canonical_path, schema_version)
VALUES (?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, backupID, artifact.Path, accountID, now, now, notes, artifact.SHA256, artifact.SizeBytes, canonicalPath, artifact.SchemaVersion); err != nil {
		return "", err
	}
	if err := s.recordAuditTx(r.Context(), tx, r, accountID, "admin_update_backup_create", "backup", backupID, fmt.Sprintf(`{"release_id":%q,"version":%q}`, release.ID, release.Version)); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	cleanupArtifact = false
	return backupID, nil
}

func removeRegularFile(filePath string) error {
	info, err := os.Lstat(filePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("backup cleanup target is not a regular file")
	}
	return os.Remove(filePath)
}

func cleanupStagedRelease(r *http.Request, manager *updatepkg.Manager, release updatepkg.Release) {
	if err := manager.DiscardRelease(release); err != nil {
		slog.ErrorContext(r.Context(), "discard staged self-update release", "release_id", release.ID, "error", err)
	}
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
	if errors.Is(err, updatepkg.ErrInvalidSignature) {
		code = "invalid_update_signature"
		message = "the update package signature is not trusted"
	}
	if errors.Is(err, updatepkg.ErrIncompatibleSchema) {
		code = "incompatible_update_schema"
		message = "the update package requires a different database schema"
	}
	if errors.Is(err, updatepkg.ErrVersionNotNewer) {
		code = "update_version_not_newer"
		message = "the update package version must be newer than the running version"
	}
	if errors.Is(err, updatepkg.ErrPendingUpdate) || errors.Is(err, updatepkg.ErrUpdateBusy) {
		code = "update_busy"
		message = "another update operation is already in progress"
	}
	if errors.Is(err, updatepkg.ErrDisabled) {
		code = "update_disabled"
		message = "self-update is not available in this deployment"
	}
	httpx.WriteError(w, r, status, code, strings.TrimSpace(message))
}
