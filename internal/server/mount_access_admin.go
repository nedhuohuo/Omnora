package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/httpx"
)

type adminMountGrantDTO struct {
	AccountID           string                 `json:"accountId"`
	Email               string                 `json:"email"`
	DisplayName         string                 `json:"displayName"`
	SpacePermission     domain.SpacePermission `json:"spacePermission"`
	Permission          *domain.SpacePermission `json:"permission"`
	EffectivePermission domain.SpacePermission `json:"effectivePermission,omitempty"`
}

type adminMountDetailDTO struct {
	ID                string               `json:"id"`
	SpaceID           string               `json:"spaceId"`
	SpaceName         string               `json:"spaceName"`
	Name              string               `json:"name"`
	DisplayName       string               `json:"displayName"`
	RootPath          string               `json:"rootPath"`
	Kind              string               `json:"kind"`
	Mode              domain.MountMode     `json:"mode"`
	IndexEnabled      bool                 `json:"indexEnabled"`
	AllowPublicShares bool                 `json:"allowPublicShares"`
	Health            string               `json:"health"`
	Status            string               `json:"status"`
	Tone              string               `json:"tone"`
	GrantCount        int                  `json:"grantCount"`
	Grants            []adminMountGrantDTO `json:"grants"`
	EligibleAccounts  []adminMountGrantDTO `json:"eligibleAccounts"`
}

func (s *Server) getAdminMount(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminMountControl(w, r) {
		return
	}
	mount, err := s.loadAdminMount(r.Context(), r.PathValue("mountId"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	detail, err := s.adminMountDetail(r, mount)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (s *Server) adminMountDetail(r *http.Request, mount adminMountRecord) (adminMountDetailDTO, error) {
	grants, eligible, err := s.loadAdminMountGrantRows(r, mount)
	if err != nil {
		return adminMountDetailDTO{}, err
	}
	return adminMountDetailDTO{
		ID: mount.ID, SpaceID: mount.SpaceID, SpaceName: mount.SpaceName,
		Name: mount.DisplayName, DisplayName: mount.DisplayName, RootPath: mount.RootPath, Kind: mount.Kind,
		Mode: mount.Mode, IndexEnabled: mount.IndexEnabled == 1,
		AllowPublicShares: mount.AllowPublicShares == 1, Health: mount.Status, Status: mount.Status,
		Tone: toneForStatus(mount.Status), GrantCount: len(grants), Grants: grants, EligibleAccounts: eligible,
	}, nil
}

func (s *Server) listAdminMountGrants(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminMountControl(w, r) {
		return
	}
	mount, err := s.loadAdminMount(r.Context(), r.PathValue("mountId"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	grants, eligible, err := s.loadAdminMountGrantRows(r, mount)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": grants, "eligibleAccounts": eligible})
}

func (s *Server) putAdminMountGrant(w http.ResponseWriter, r *http.Request) {
	session, ok := s.adminMountControlSession(w, r)
	if !ok {
		return
	}
	mount, err := s.loadAdminMount(r.Context(), r.PathValue("mountId"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	var req struct {
		Permission domain.SpacePermission `json:"permission"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Permission.Valid() {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "permission must be viewer, editor, or manager")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `
INSERT INTO mount_account_grants(mount_id, account_id, permission, created_at, updated_at)
SELECT m.id, sm.account_id, ?, ?, ?
FROM mounts m
JOIN spaces sp ON sp.id = m.space_id AND sp.status = 'active'
JOIN space_members sm ON sm.space_id = m.space_id AND sm.account_id = ?
JOIN accounts a ON a.id = sm.account_id AND a.status = 'active'
WHERE m.id = ? AND m.space_id = ? AND m.status <> 'deleted'
ON CONFLICT(mount_id, account_id) DO UPDATE SET permission = excluded.permission, updated_at = excluded.updated_at
`, req.Permission, now, now, accountID, mount.ID, mount.SpaceID)
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
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM mount_account_grants WHERE mount_id = ? AND account_id = ?`, mount.ID, accountID); err != nil {
			writeDBError(w, r, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `
UPDATE shares SET revoked_at = COALESCE(revoked_at, ?), updated_at = ?
WHERE mount_id = ? AND creator_account_id = ? AND revoked_at IS NULL
`, now, now, mount.ID, accountID); err != nil {
			writeDBError(w, r, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `
UPDATE upload_sessions SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?)
WHERE mount_id = ? AND account_id = ? AND status = 'active'
`, now, mount.ID, accountID); err != nil {
			writeDBError(w, r, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `
DELETE FROM ai_token_boundaries
WHERE mount_id = ? AND token_id IN (SELECT id FROM ai_tokens WHERE account_id = ?)
`, mount.ID, accountID); err != nil {
			writeDBError(w, r, err)
			return
		}
		if err := tx.Commit(); err != nil {
			writeDBError(w, r, err)
			return
		}
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_grant", "account must be an active member of the mount space")
		return
	}
	var permission domain.SpacePermission
	if err := tx.QueryRowContext(r.Context(), `
SELECT sm.permission
FROM space_members sm
JOIN accounts a ON a.id = sm.account_id AND a.status = 'active'
JOIN spaces sp ON sp.id = sm.space_id AND sp.status = 'active'
WHERE sm.space_id = ? AND sm.account_id = ?
`, mount.SpaceID, accountID).Scan(&permission); err != nil {
		writeDBError(w, r, err)
		return
	}
	if req.Permission != domain.SpacePermissionManager || permission != domain.SpacePermissionManager {
		if _, err := tx.ExecContext(r.Context(), `
UPDATE shares SET revoked_at = COALESCE(revoked_at, ?), updated_at = ?
WHERE mount_id = ? AND creator_account_id = ? AND revoked_at IS NULL
`, now, now, mount.ID, accountID); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	if req.Permission == domain.SpacePermissionViewer || permission == domain.SpacePermissionViewer {
		if _, err := tx.ExecContext(r.Context(), `
UPDATE upload_sessions SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?)
WHERE mount_id = ? AND account_id = ? AND status = 'active'
`, now, mount.ID, accountID); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "mount_grant_update", "mount", mount.ID, fmt.Sprintf(`{"accountId":%q,"permission":%q,"updatedBy":%q}`, accountID, req.Permission, session.AccountID))
	grant, err := s.loadAdminMountGrant(r, mount.ID, accountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, grant)
}

func (s *Server) deleteAdminMountGrant(w http.ResponseWriter, r *http.Request) {
	session, ok := s.adminMountControlSession(w, r)
	if !ok {
		return
	}
	mount, err := s.loadAdminMount(r.Context(), r.PathValue("mountId"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `DELETE FROM mount_account_grants WHERE mount_id = ? AND account_id = ?`, mount.ID, accountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount grant was not found")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE shares SET revoked_at = COALESCE(revoked_at, ?), updated_at = ?
WHERE mount_id = ? AND creator_account_id = ? AND revoked_at IS NULL
`, now, now, mount.ID, accountID); err != nil {
		writeDBError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE upload_sessions SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?)
WHERE mount_id = ? AND account_id = ? AND status = 'active'
`, now, mount.ID, accountID); err != nil {
		writeDBError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
DELETE FROM ai_token_boundaries
WHERE mount_id = ? AND token_id IN (SELECT id FROM ai_tokens WHERE account_id = ?)
`, mount.ID, accountID); err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "mount_grant_delete", "mount", mount.ID, fmt.Sprintf(`{"accountId":%q,"deletedBy":%q}`, accountID, session.AccountID))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireAdminMountControl(w http.ResponseWriter, r *http.Request) bool {
	_, ok := s.adminMountControlSession(w, r)
	return ok
}

func (s *Server) adminMountControlSession(w http.ResponseWriter, r *http.Request) (identitySession, bool) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return identitySession{}, false
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can manage mount access")
		return identitySession{}, false
	}
	return identitySession{AccountID: session.AccountID}, true
}

type identitySession struct {
	AccountID string
}

func (s *Server) loadAdminMountGrantRows(r *http.Request, mount adminMountRecord) ([]adminMountGrantDTO, []adminMountGrantDTO, error) {
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT a.id, a.email, a.display_name, sm.permission, mg.permission
FROM space_members sm
JOIN accounts a ON a.id = sm.account_id
LEFT JOIN mount_account_grants mg ON mg.mount_id = ? AND mg.account_id = a.id
WHERE sm.space_id = ? AND a.status = 'active'
ORDER BY lower(a.display_name), lower(a.email), a.id
`, mount.ID, mount.SpaceID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	grants := []adminMountGrantDTO{}
	eligible := []adminMountGrantDTO{}
	for rows.Next() {
		var item adminMountGrantDTO
		var permission sql.NullString
		if err := rows.Scan(&item.AccountID, &item.Email, &item.DisplayName, &item.SpacePermission, &permission); err != nil {
			return nil, nil, err
		}
		if permission.Valid {
			value := domain.SpacePermission(permission.String)
			item.Permission = &value
			item.EffectivePermission = minimumServerPermission(item.SpacePermission, value)
			grants = append(grants, item)
		} else {
			eligible = append(eligible, item)
		}
	}
	return grants, eligible, rows.Err()
}

func (s *Server) loadAdminMountGrant(r *http.Request, mountID, accountID string) (adminMountGrantDTO, error) {
	var item adminMountGrantDTO
	var permission domain.SpacePermission
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT a.id, a.email, a.display_name, sm.permission, mg.permission
FROM mount_account_grants mg
JOIN accounts a ON a.id = mg.account_id
JOIN mounts m ON m.id = mg.mount_id
JOIN space_members sm ON sm.space_id = m.space_id AND sm.account_id = a.id
WHERE mg.mount_id = ? AND mg.account_id = ?
`, mountID, accountID).Scan(&item.AccountID, &item.Email, &item.DisplayName, &item.SpacePermission, &permission)
	if err != nil {
		return adminMountGrantDTO{}, err
	}
	item.Permission = &permission
	item.EffectivePermission = minimumServerPermission(item.SpacePermission, permission)
	return item, nil
}

func minimumServerPermission(left, right domain.SpacePermission) domain.SpacePermission {
	if accessRank(left) <= accessRank(right) {
		return left
	}
	return right
}
