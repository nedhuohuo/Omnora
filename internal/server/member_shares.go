package server

import (
	"database/sql"
	"net/http"
	"time"

	"omnora/internal/access"
	"omnora/internal/domain"
	"omnora/internal/httpx"
)

type shareRecordDTO struct {
	ID                 string `json:"id"`
	PublicID           string `json:"publicId"`
	SpaceID            string `json:"spaceId"`
	SpaceName          string `json:"spaceName,omitempty"`
	MountID            string `json:"mountId"`
	MountName          string `json:"mountName,omitempty"`
	RelativePath       string `json:"relativePath"`
	CreatorEmail       string `json:"creatorEmail,omitempty"`
	CreatorDisplayName string `json:"creatorDisplayName,omitempty"`
	AllowPreview       bool   `json:"allowPreview"`
	AllowDownload      bool   `json:"allowDownload"`
	MaxVisits          *int64 `json:"maxVisits,omitempty"`
	UsedVisits         int    `json:"usedVisits"`
	MaxDownloads       *int64 `json:"maxDownloads,omitempty"`
	UsedDownloads      int    `json:"usedDownloads"`
	ExpiresAt          string `json:"expiresAt"`
	RevokedAt          string `json:"revokedAt,omitempty"`
	Status             string `json:"status"`
}

// listShares returns share metadata only where the current account still has
// effective manager permission on the target mount.
func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
	SELECT sh.id, sh.public_id, sh.space_id, sh.mount_id, sh.relative_path,
	       sh.allow_preview, sh.allow_download, sh.max_visits, sh.used_visits,
	       sh.max_downloads, sh.used_downloads, sh.expires_at, COALESCE(sh.revoked_at, ''),
	       COALESCE(sp.name, ''), COALESCE(m.display_name, ''), COALESCE(a.email, ''), COALESCE(a.display_name, '')
FROM shares sh
JOIN spaces sp ON sp.id = sh.space_id
JOIN mounts m ON m.id = sh.mount_id
JOIN accounts a ON a.id = sh.creator_account_id
WHERE EXISTS (
  SELECT 1
  FROM accounts current_account
  JOIN space_members sm ON sm.account_id = current_account.id AND sm.space_id = sh.space_id
  JOIN mount_account_grants mg ON mg.account_id = current_account.id AND mg.mount_id = sh.mount_id
  WHERE current_account.id = ? AND current_account.status = 'active'
    AND sm.permission = 'manager' AND mg.permission = 'manager'
    AND sp.status = 'active' AND m.status = 'active'
)
ORDER BY sh.created_at DESC
LIMIT ?
`, session.AccountID, parseIntDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()

	items := []shareRecordDTO{}
	for rows.Next() {
		item, err := scanShareRecordDTO(rows)
		if err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// revokeShare revokes a share if the current account is the creator or a
// manager of the space the share belongs to.
func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	shareID := r.PathValue("shareId")
	var spaceID, mountID, creatorAccountID string
	err = s.sqlDB().QueryRowContext(r.Context(), `
SELECT space_id, mount_id, creator_account_id
FROM shares
WHERE id = ? AND revoked_at IS NULL
`, shareID).Scan(&spaceID, &mountID, &creatorAccountID)
	if err == sql.ErrNoRows {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "share was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	authorized := creatorAccountID == session.AccountID
	if !authorized {
		decision, authErr := s.authorizeMount(r, session.AccountID, spaceID, mountID, access.OperationRead)
		authorized = authErr == nil && decision.EffectivePermission == domain.SpacePermissionManager
	}
	if !authorized {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only the creator or a space manager can revoke this share")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE shares
SET revoked_at = ?, updated_at = ?
WHERE id = ? AND revoked_at IS NULL
`, now, now, shareID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "share was not found")
		return
	}
	_ = s.recordAudit(r, "share_revoke", "share", shareID, "{}")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) isSpaceManager(r *http.Request, accountID, spaceID string) bool {
	var count int
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT COUNT(1)
FROM space_members sm
JOIN spaces sp ON sp.id = sm.space_id
WHERE sm.account_id = ? AND sm.space_id = ? AND sm.permission = 'manager' AND sp.status = 'active'
`, accountID, spaceID).Scan(&count)
	return err == nil && count == 1
}

func scanShareRecordDTO(rows *sql.Rows) (shareRecordDTO, error) {
	var item shareRecordDTO
	var allowPreview, allowDownload int
	var maxVisits, maxDownloads sql.NullInt64
	if err := rows.Scan(
		&item.ID, &item.PublicID, &item.SpaceID, &item.MountID, &item.RelativePath,
		&allowPreview, &allowDownload, &maxVisits, &item.UsedVisits,
		&maxDownloads, &item.UsedDownloads, &item.ExpiresAt, &item.RevokedAt,
		&item.SpaceName, &item.MountName, &item.CreatorEmail, &item.CreatorDisplayName,
	); err != nil {
		return shareRecordDTO{}, err
	}
	item.AllowPreview = allowPreview == 1
	item.AllowDownload = allowDownload == 1
	if maxVisits.Valid {
		item.MaxVisits = &maxVisits.Int64
	}
	if maxDownloads.Valid {
		item.MaxDownloads = &maxDownloads.Int64
	}
	item.Status = shareRecordStatus(item.ExpiresAt, item.RevokedAt)
	return item, nil
}

func shareRecordStatus(expiresAt, revokedAt string) string {
	if revokedAt != "" {
		return "revoked"
	}
	if parsed, err := time.Parse(time.RFC3339Nano, expiresAt); err == nil && !time.Now().UTC().Before(parsed) {
		return "expired"
	}
	return "active"
}
