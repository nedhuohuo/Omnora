package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/mountid"
)

type adminMountRecord struct {
	ID                string
	SpaceID           string
	SpaceName         string
	DisplayName       string
	RootPath          string
	Kind              string
	Mode              domain.MountMode
	IndexEnabled      int
	AllowPublicShares int
	Status            string
}

func (s *Server) renameMount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can rename mounts")
		return
	}

	var req struct {
		DisplayName       *string           `json:"displayName"`
		DisplayNameAlt    *string           `json:"display_name"`
		Mode              *domain.MountMode `json:"mode"`
		IndexEnabled      *bool             `json:"indexEnabled"`
		AllowPublicShares *bool             `json:"allowPublicShares"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == nil {
		req.DisplayName = req.DisplayNameAlt
	}
	if req.DisplayName == nil && req.Mode == nil && req.IndexEnabled == nil && req.AllowPublicShares == nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "at least one mount setting is required")
		return
	}

	mountID := strings.TrimSpace(r.PathValue("mountId"))
	mount, err := s.loadAdminMount(r.Context(), mountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}

	displayName := mount.DisplayName
	if req.DisplayName != nil {
		displayName, err = normalizeMountDisplayName(*req.DisplayName)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
	}
	mode := mount.Mode
	if req.Mode != nil {
		mode = *req.Mode
		if mode != domain.MountModeReadOnly && mode != domain.MountModeReadWrite {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "mount mode must be read_only or read_write")
			return
		}
		if mode == domain.MountModeReadWrite {
			if err := probeMountWritable(mount.RootPath); err != nil {
				httpx.WriteError(w, r, http.StatusConflict, "mount_not_writable", err.Error())
				return
			}
		}
	}
	indexEnabled := mount.IndexEnabled
	if req.IndexEnabled != nil {
		indexEnabled = boolInt(*req.IndexEnabled)
	}
	allowPublicShares := mount.AllowPublicShares
	if req.AllowPublicShares != nil {
		allowPublicShares = boolInt(*req.AllowPublicShares)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `
UPDATE mounts
SET display_name = ?, mode = ?, index_enabled = ?, allow_public_shares = ?, updated_at = ?
WHERE id = ? AND status <> 'deleted'
`, displayName, mode, indexEnabled, allowPublicShares, now, mount.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			httpx.WriteError(w, r, http.StatusConflict, "mount_conflict", "mount display name already exists in this space")
			return
		}
		writeDBError(w, r, err)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if req.AllowPublicShares != nil && !*req.AllowPublicShares {
		if _, err := tx.ExecContext(r.Context(), `
UPDATE shares SET revoked_at = COALESCE(revoked_at, ?), updated_at = ?
WHERE mount_id = ? AND revoked_at IS NULL
`, now, now, mount.ID); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	if req.Mode != nil && *req.Mode == domain.MountModeReadOnly {
		if _, err := tx.ExecContext(r.Context(), `
UPDATE upload_sessions SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?)
WHERE mount_id = ? AND status = 'active'
`, now, mount.ID); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}

	_ = s.recordAudit(r, "mount_update", "mount", mount.ID, fmt.Sprintf(`{"from":%q,"to":%q,"mode":%q,"indexEnabled":%t,"allowPublicShares":%t}`, mount.DisplayName, displayName, mode, indexEnabled == 1, allowPublicShares == 1))
	updated, err := s.loadAdminMount(r.Context(), mount.ID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	detail, err := s.adminMountDetail(r, updated)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (s *Server) reverifyMount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can re-verify mounts")
		return
	}

	mountRegistrationMu.Lock()
	defer mountRegistrationMu.Unlock()

	mountID := strings.TrimSpace(r.PathValue("mountId"))
	mount, err := s.loadAdminMount(r.Context(), mountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}

	existing, err := s.loadMountIdentities(r)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	filtered := make([]mountid.Identity, 0, len(existing))
	for _, identity := range existing {
		if filepath.Clean(identity.Path) == filepath.Clean(mount.RootPath) {
			continue
		}
		filtered = append(filtered, identity)
	}
	identity, err := mountid.VerifyCandidateRoot(mount.RootPath, filtered)
	if err != nil {
		status := http.StatusConflict
		code := "mount_identity_unverifiable"
		if errors.Is(err, mountid.ErrMountConflict) {
			code = "mount_conflict"
		}
		httpx.WriteError(w, r, status, code, err.Error())
		return
	}
	inferredKind, err := s.inferMountKind(identity.Path)
	if err != nil || inferredKind != mount.Kind {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_mount_root", "mount root is outside its configured allowed storage root")
		return
	}
	if mount.Mode == domain.MountModeReadWrite {
		if err := probeMountWritable(identity.Path); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
	}

	identityJSON, err := json.Marshal(identity)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.sqlDB().ExecContext(r.Context(), `
UPDATE mounts
SET status = 'active', root_path = ?, mount_identity_json = ?, updated_at = ?
WHERE id = ? AND status <> 'deleted'
`, identity.Path, string(identityJSON), now, mount.ID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}

	_ = s.recordAudit(r, "mount_reverify", "mount", mount.ID, "{}")
	httpx.WriteJSON(w, http.StatusOK, mountDTO{
		ID: mount.ID, Name: mount.DisplayName, Space: mount.SpaceName, Mode: displayMountMode(mount.Mode),
		Index: displayIndex(mount.IndexEnabled), Health: "active", Tone: "ok",
	})
}

func (s *Server) deleteMount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can delete mounts")
		return
	}

	var req struct {
		DeleteData    bool `json:"deleteData"`
		DeleteDataAlt bool `json:"delete_data"`
	}
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	deleteData := req.DeleteData || req.DeleteDataAlt

	mountID := strings.TrimSpace(r.PathValue("mountId"))
	mount, err := s.loadAdminMount(r.Context(), mountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}

	if err := s.softDeleteMount(r.Context(), mount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
			return
		}
		writeDBError(w, r, err)
		return
	}

	dataDeleted := false
	if deleteData {
		if err := wipeMountContents(mount.RootPath); err != nil {
			httpx.WriteError(w, r, http.StatusConflict, "mount_data_delete_failed", fmt.Sprintf("mount was deleted but folder data could not be removed: %v", err))
			return
		}
		dataDeleted = true
	}

	metadata, _ := json.Marshal(map[string]any{
		"deleteData":  deleteData,
		"dataDeleted": dataDeleted,
		"rootPath":    mount.RootPath,
	})
	_ = s.recordAudit(r, "mount_delete", "mount", mount.ID, string(metadata))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":          mount.ID,
		"deleted":     true,
		"deleteData":  deleteData,
		"dataDeleted": dataDeleted,
	})
}

func (s *Server) softDeleteMount(ctx context.Context, mount adminMountRecord) error {
	tx, err := s.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	deletedName := deletedMountDisplayName(mount.DisplayName, mount.ID)
	result, err := tx.ExecContext(ctx, `
UPDATE mounts
SET status = 'deleted', display_name = ?, updated_at = ?
WHERE id = ? AND status <> 'deleted'
`, deletedName, now, mount.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_entries WHERE mount_id = ?`, mount.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM file_objects WHERE mount_id = ?`, mount.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE shares
SET revoked_at = COALESCE(revoked_at, ?), updated_at = ?
WHERE mount_id = ? AND revoked_at IS NULL
`, now, now, mount.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_token_boundaries WHERE mount_id = ?`, mount.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mount_account_grants WHERE mount_id = ?`, mount.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE upload_sessions
SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?)
WHERE mount_id = ? AND status = 'active'
`, now, mount.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = 'canceled', updated_at = ?, completed_at = COALESCE(completed_at, ?)
WHERE status IN ('queued', 'running', 'paused')
  AND (
    instr(payload_json, ?) > 0
    OR instr(payload_json, ?) > 0
  )
`, now, now, `"mountId":"`+mount.ID+`"`, `"mount_id":"`+mount.ID+`"`); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Server) loadAdminMount(ctx context.Context, mountID string) (adminMountRecord, error) {
	mountID = strings.TrimSpace(mountID)
	if mountID == "" {
		return adminMountRecord{}, sql.ErrNoRows
	}
	var mount adminMountRecord
	err := s.sqlDB().QueryRowContext(ctx, `
SELECT m.id, m.space_id, sp.name, m.display_name, m.root_path, m.kind, m.mode, m.index_enabled, m.allow_public_shares, m.status
FROM mounts m
JOIN spaces sp ON sp.id = m.space_id
WHERE m.id = ? AND m.status <> 'deleted'
`, mountID).Scan(
		&mount.ID,
		&mount.SpaceID,
		&mount.SpaceName,
		&mount.DisplayName,
		&mount.RootPath,
		&mount.Kind,
		&mount.Mode,
		&mount.IndexEnabled,
		&mount.AllowPublicShares,
		&mount.Status,
	)
	return mount, err
}

func normalizeMountDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("displayName is required")
	}
	if utf8.RuneCountInString(value) > 128 {
		return "", fmt.Errorf("displayName is too long")
	}
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("displayName contains invalid characters")
		}
	}
	return value, nil
}

func deletedMountDisplayName(displayName, mountID string) string {
	suffix := "__deleted__" + mountID
	maxRunes := 128
	base := strings.TrimSpace(displayName)
	if base == "" {
		base = "mount"
	}
	runes := []rune(base)
	available := maxRunes - utf8.RuneCountInString(suffix)
	if available < 1 {
		return suffix[len(suffix)-maxRunes:]
	}
	if len(runes) > available {
		runes = runes[:available]
	}
	return string(runes) + suffix
}

func wipeMountContents(rootPath string) error {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if rootPath == "" || rootPath == "." || rootPath == string(filepath.Separator) {
		return fmt.Errorf("refusing to wipe unsafe mount root")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer root.Close()

	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		if err := root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return false
	}
	return true
}
