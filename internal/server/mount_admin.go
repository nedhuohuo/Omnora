package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/mountid"
)

type adminMountRecord struct {
	ID           string
	SpaceID      string
	SpaceName    string
	DisplayName  string
	RootPath     string
	Kind         string
	Mode         domain.MountMode
	IndexEnabled int
	Status       string
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
		DisplayName    string `json:"displayName"`
		DisplayNameAlt string `json:"display_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.DisplayNameAlt
	}
	displayName, err := normalizeMountDisplayName(req.DisplayName)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
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

	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE mounts
SET display_name = ?, updated_at = ?
WHERE id = ? AND status <> 'deleted'
`, displayName, now, mount.ID)
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

	_ = s.recordAudit(r, "mount_rename", "mount", mount.ID, fmt.Sprintf(`{"from":%q,"to":%q}`, mount.DisplayName, displayName))
	httpx.WriteJSON(w, http.StatusOK, mountDTO{
		ID: mount.ID, Name: displayName, Space: mount.SpaceName, Mode: displayMountMode(mount.Mode),
		Index: displayIndex(mount.IndexEnabled), Health: mount.Status, Tone: toneForStatus(mount.Status),
	})
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
	if mount.Mode == domain.MountModeReadWrite {
		if err := probeMountWritable(identity.Path); err != nil {
			httpx.WriteError(w, r, http.StatusConflict, "mount_not_writable", err.Error())
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
	if deleteData {
		httpx.WriteError(w, r, http.StatusBadRequest, "mount_data_delete_disabled", "deleting a mount never deletes files or directories")
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

	if err := s.softDeleteMount(r.Context(), mount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
			return
		}
		writeDBError(w, r, err)
		return
	}

	metadata, _ := json.Marshal(map[string]any{
		"deleteData":  false,
		"dataDeleted": false,
		"rootPath":    mount.RootPath,
	})
	_ = s.recordAudit(r, "mount_delete", "mount", mount.ID, string(metadata))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":          mount.ID,
		"deleted":     true,
		"deleteData":  false,
		"dataDeleted": false,
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
SELECT m.id, m.space_id, sp.name, m.display_name, m.root_path, m.kind, m.mode, m.index_enabled, m.status
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
