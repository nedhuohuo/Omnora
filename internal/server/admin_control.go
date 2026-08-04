package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/store"
)

// requireAdmin resolves the current session and confirms the account is a
// system administrator, writing an appropriate error response otherwise.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (identity.Session, bool) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return identity.Session{}, false
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can perform this action")
		return identity.Session{}, false
	}
	return session, true
}

// ---- Overview ----

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	db := s.sqlDB()

	accountsByStatus, err := countGroupedBy(r, db, "SELECT status, COUNT(1) FROM accounts GROUP BY status")
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	var spaceCount int
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM spaces WHERE status = 'active'").Scan(&spaceCount); err != nil {
		writeDBError(w, r, err)
		return
	}
	mountsByHealth, err := countGroupedBy(r, db, "SELECT status, COUNT(1) FROM mounts WHERE status <> 'deleted' GROUP BY status")
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	jobsByStatus, err := countGroupedBy(r, db, "SELECT status, COUNT(1) FROM jobs GROUP BY status")
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	latestBackup, err := s.loadLatestBackup(r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeDBError(w, r, err)
		return
	}

	risks := []string{}
	var unavailableMounts int
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM mounts WHERE status = 'unavailable'").Scan(&unavailableMounts); err == nil && unavailableMounts > 0 {
		risks = append(risks, fmt.Sprintf("%d mount(s) require re-verification", unavailableMounts))
	}
	var adminsWithoutTOTP int
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM accounts WHERE role = 'admin' AND status = 'active' AND totp_required = 0").Scan(&adminsWithoutTOTP); err == nil && adminsWithoutTOTP > 0 {
		risks = append(risks, fmt.Sprintf("%d administrator account(s) do not have TOTP enabled", adminsWithoutTOTP))
	}
	if latestBackup == nil {
		risks = append(risks, "no backups have been recorded yet")
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"accounts":       accountsByStatus,
		"spaces":         spaceCount,
		"mountsByHealth": mountsByHealth,
		"jobsByStatus":   jobsByStatus,
		"routeGroups":    s.routeGroups(),
		"latestBackup":   latestBackup,
		"risks":          risks,
	})
}

func countGroupedBy(r *http.Request, db *sql.DB, query string) (map[string]int, error) {
	rows, err := db.QueryContext(r.Context(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		result[key] = count
	}
	return result, rows.Err()
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// ---- Users ----

type adminUserDTO struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	TOTPRequired bool   `json:"totpRequired"`
}

func (s *Server) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT id, email, display_name, role, status, totp_required
FROM accounts
WHERE status <> 'deleted'
ORDER BY created_at
`)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := []adminUserDTO{}
	for rows.Next() {
		var item adminUserDTO
		var totpRequired int
		if err := rows.Scan(&item.ID, &item.Email, &item.DisplayName, &item.Role, &item.Status, &totpRequired); err != nil {
			writeDBError(w, r, err)
			return
		}
		item.TOTPRequired = totpRequired == 1
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAdminUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var req struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	role := domain.AccountRoleMember
	if req.Role == string(domain.AccountRoleAdmin) {
		role = domain.AccountRoleAdmin
	}
	created, err := identity.New(s.sqlDB(), identity.Options{}).CreateAccount(r.Context(), identity.CreateAccountRequest{
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
		Role:        role,
	})
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "admin_user_create", "account", created.Account.ID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"user":  accountResponse(created.Account),
		"space": spaceResponse(created.PersonalSpace, domain.SpacePermissionManager),
	})
}

func (s *Server) setAdminUserEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	userID := r.PathValue("userId")
	status := "disabled"
	if enabled {
		status = "active"
	}
	now := nowRFC3339()
	result, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE accounts SET status = ?, updated_at = ? WHERE id = ? AND status <> 'deleted'
`, status, now, userID)
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
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "user was not found")
		return
	}
	if !enabled {
		_ = identity.New(s.sqlDB(), identity.Options{}).RevokeAllSessions(r.Context(), userID, "")
	}
	_ = s.recordAudit(r, "admin_user_"+status, "account", userID, "{}")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": userID, "status": status})
}

func (s *Server) disableAdminUser(w http.ResponseWriter, r *http.Request) {
	s.setAdminUserEnabled(w, r, false)
}

func (s *Server) enableAdminUser(w http.ResponseWriter, r *http.Request) {
	s.setAdminUserEnabled(w, r, true)
}

func (s *Server) revokeAdminUserSessions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	userID := r.PathValue("userId")
	if err := identity.New(s.sqlDB(), identity.Options{}).RevokeAllSessions(r.Context(), userID, ""); err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "admin_user_revoke_sessions", "account", userID, "{}")
	w.WriteHeader(http.StatusNoContent)
}

// ---- Space membership ----

type spaceMemberDTO struct {
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Permission  string `json:"permission"`
}

func (s *Server) listAdminSpaceMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	spaceID := r.PathValue("spaceId")
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT sm.account_id, a.email, a.display_name, sm.permission
FROM space_members sm
JOIN accounts a ON a.id = sm.account_id
WHERE sm.space_id = ?
ORDER BY a.display_name
`, spaceID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := []spaceMemberDTO{}
	for rows.Next() {
		var item spaceMemberDTO
		if err := rows.Scan(&item.AccountID, &item.Email, &item.DisplayName, &item.Permission); err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) putAdminSpaceMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	spaceID := r.PathValue("spaceId")
	accountRef := strings.TrimSpace(r.PathValue("accountId"))
	if accountRef == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "account id or email is required")
		return
	}
	var req struct {
		Permission string `json:"permission"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.requireActiveAdminSpace(w, r, spaceID) {
		return
	}
	permission := domain.SpacePermission(req.Permission)
	switch permission {
	case domain.SpacePermissionViewer, domain.SpacePermissionEditor, domain.SpacePermissionManager:
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "permission must be viewer, editor, or manager")
		return
	}
	member, ok := s.resolveAdminSpaceMemberAccount(w, r, accountRef)
	if !ok {
		return
	}
	now := nowRFC3339()
	_, err := s.sqlDB().ExecContext(r.Context(), `
INSERT INTO space_members(space_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(space_id, account_id) DO UPDATE SET permission = excluded.permission, updated_at = excluded.updated_at
`, spaceID, member.AccountID, string(permission), now, now)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "admin_space_member_set", "space", spaceID, fmt.Sprintf(`{"accountId":%q,"permission":%q}`, member.AccountID, permission))
	member.Permission = string(permission)
	httpx.WriteJSON(w, http.StatusOK, member)
}

func (s *Server) requireActiveAdminSpace(w http.ResponseWriter, r *http.Request, spaceID string) bool {
	var exists int
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT 1
FROM spaces
WHERE id = ? AND status = 'active'
`, spaceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "space was not found")
		return false
	}
	if err != nil {
		writeDBError(w, r, err)
		return false
	}
	return true
}

func (s *Server) resolveAdminSpaceMemberAccount(w http.ResponseWriter, r *http.Request, accountRef string) (spaceMemberDTO, bool) {
	var member spaceMemberDTO
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT id, email, display_name
FROM accounts
WHERE id = ? OR email = ?
`, accountRef, strings.ToLower(accountRef)).Scan(&member.AccountID, &member.Email, &member.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "account was not found")
		return spaceMemberDTO{}, false
	}
	if err != nil {
		writeDBError(w, r, err)
		return spaceMemberDTO{}, false
	}
	return member, true
}

func (s *Server) deleteAdminSpaceMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	spaceID := r.PathValue("spaceId")
	accountID := r.PathValue("accountId")
	result, err := s.sqlDB().ExecContext(r.Context(), `
DELETE FROM space_members WHERE space_id = ? AND account_id = ?
`, spaceID, accountID)
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
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "membership was not found")
		return
	}
	_ = s.recordAudit(r, "admin_space_member_remove", "space", spaceID, fmt.Sprintf(`{"accountId":%q}`, accountID))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createAdminSpace(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "name is required")
		return
	}
	spaceID := "spc_" + httpx.NewRequestID()
	now := nowRFC3339()
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO spaces(id, kind, name, owner_account_id, status, created_at, updated_at)
VALUES (?, 'shared', ?, ?, 'active', ?, ?)
`, spaceID, name, session.AccountID, now, now); err != nil {
		writeDBError(w, r, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO space_members(space_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, 'manager', ?, ?)
`, spaceID, session.AccountID, now, now); err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "admin_space_create", "space", spaceID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": spaceID, "type": "shared", "name": name})
}

// ---- Global shares & AI tokens ----

func (s *Server) listAdminShares(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
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
ORDER BY sh.created_at DESC
LIMIT ?
`, parseIntDefault(r.URL.Query().Get("limit"), 200))
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revokeAdminShare(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	shareID := r.PathValue("shareId")
	now := nowRFC3339()
	result, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE shares SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL
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
	_ = s.recordAudit(r, "admin_share_revoke", "share", shareID, "{}")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAdminAITokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT t.id, t.public_id, t.account_id, t.name, t.scopes, t.created_at, t.expires_at,
       COALESCE(t.last_used_at, ''), COALESCE(t.revoked_at, ''),
       COALESCE(a.email, ''), COALESCE(a.display_name, '')
FROM ai_tokens t
LEFT JOIN accounts a ON a.id = t.account_id
ORDER BY t.created_at DESC
LIMIT ?
`, parseIntDefault(r.URL.Query().Get("limit"), 200))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, publicID, accountID, name, scopesJSON, createdAt, expiresAt, lastUsedAt, revokedAt, accountEmail, accountDisplayName string
		if err := rows.Scan(&id, &publicID, &accountID, &name, &scopesJSON, &createdAt, &expiresAt, &lastUsedAt, &revokedAt, &accountEmail, &accountDisplayName); err != nil {
			writeDBError(w, r, err)
			return
		}
		item := aiTokenResponse(id, publicID, name, scopesJSON, createdAt, expiresAt, lastUsedAt, revokedAt, []map[string]string{})
		item["accountId"] = accountID
		item["accountEmail"] = accountEmail
		item["accountDisplayName"] = accountDisplayName
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revokeAdminAIToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	tokenID := r.PathValue("tokenId")
	now := nowRFC3339()
	result, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE ai_tokens SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL
`, now, now, tokenID)
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
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "AI token was not found")
		return
	}
	_ = s.recordAudit(r, "admin_ai_token_revoke", "ai_token", tokenID, "{}")
	w.WriteHeader(http.StatusNoContent)
}

// ---- Backups ----

type backupDTO struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Path        string `json:"path,omitempty"`
	CreatedBy   string `json:"createdBy,omitempty"`
	CreatedAt   string `json:"createdAt"`
	CompletedAt string `json:"completedAt,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT id, status, COALESCE(path, ''), COALESCE(created_by, ''), created_at, COALESCE(completed_at, ''), notes
FROM backups
ORDER BY created_at DESC
LIMIT ?
`, parseIntDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := []backupDTO{}
	for rows.Next() {
		var item backupDTO
		if err := rows.Scan(&item.ID, &item.Status, &item.Path, &item.CreatedBy, &item.CreatedAt, &item.CompletedAt, &item.Notes); err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) loadLatestBackup(r *http.Request) (*backupDTO, error) {
	var item backupDTO
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT id, status, COALESCE(path, ''), COALESCE(created_by, ''), created_at, COALESCE(completed_at, ''), notes
FROM backups
ORDER BY created_at DESC
LIMIT 1
`).Scan(&item.ID, &item.Status, &item.Path, &item.CreatedBy, &item.CreatedAt, &item.CompletedAt, &item.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	id := "bkp_" + httpx.NewRequestID()
	now := time.Now().UTC()
	status := "completed"
	notes := "sqlite online backup"
	backupPath := ""
	completedAt := any(now.Format(time.RFC3339Nano))

	if s.db == nil || strings.TrimSpace(s.cfg.Database.Path) == "" {
		status = "failed"
		notes = "database is not configured"
		completedAt = nil
	} else {
		backupsDir := filepath.Join(strings.TrimSpace(s.cfg.Storage.ManagedDir), "backups")
		if err := os.MkdirAll(backupsDir, 0o755); err != nil {
			status = "failed"
			notes = "could not prepare backups directory: " + err.Error()
			completedAt = nil
		} else {
			target := filepath.Join(backupsDir, fmt.Sprintf("omnora-%s.db", now.Format("20060102T150405Z0700")))
			if err := s.db.BackupTo(r.Context(), target); err != nil {
				status = "failed"
				notes = "online backup failed: " + err.Error()
				completedAt = nil
				_ = os.Remove(target)
			} else if err := verifyBackupFile(r.Context(), target); err != nil {
				status = "failed"
				notes = "backup integrity check failed: " + err.Error()
				completedAt = nil
				_ = os.Remove(target)
			} else {
				backupPath = target
			}
		}
	}

	_, err := s.sqlDB().ExecContext(r.Context(), `
INSERT INTO backups(id, status, path, created_by, created_at, completed_at, notes)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, status, nullIfEmpty(backupPath), session.AccountID, now.Format(time.RFC3339Nano), completedAt, notes)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "admin_backup_create", "backup", id, "{}")
	httpx.WriteJSON(w, http.StatusCreated, backupDTO{
		ID: id, Status: status, Path: backupPath, CreatedBy: session.AccountID,
		CreatedAt: now.Format(time.RFC3339Nano), CompletedAt: stringOrEmpty(completedAt), Notes: notes,
	})
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	backupID := r.PathValue("backupId")
	var req struct {
		ConfirmPhrase string `json:"confirmPhrase"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ConfirmPhrase) != "RESTORE" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "confirmPhrase must be RESTORE")
		return
	}
	var item backupDTO
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT id, status, COALESCE(path, ''), COALESCE(created_by, ''), created_at, COALESCE(completed_at, ''), notes
FROM backups WHERE id = ?
`, backupID).Scan(&item.ID, &item.Status, &item.Path, &item.CreatedBy, &item.CreatedAt, &item.CompletedAt, &item.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "backup was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if item.Status != "completed" || strings.TrimSpace(item.Path) == "" {
		httpx.WriteError(w, r, http.StatusConflict, "backup_not_restorable", "backup is not a completed file snapshot")
		return
	}
	if _, err := os.Stat(item.Path); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "backup_missing", "backup file is missing on disk")
		return
	}
	if err := verifyBackupFile(r.Context(), item.Path); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "backup_corrupt", err.Error())
		return
	}
	if s.db == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is required")
		return
	}
	if err := s.db.RestoreFrom(r.Context(), item.Path); err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "restore_failed", err.Error())
		return
	}
	if err := s.db.IntegrityCheck(r.Context()); err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "restore_integrity_failed", err.Error())
		return
	}
	_ = s.hydrateRouteGroups(r.Context())
	_ = s.recordAudit(r, "admin_backup_restore", "backup", backupID, fmt.Sprintf(`{"by":%q}`, session.AccountID))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "restored",
		"backupId": backupID,
		"notes":    "database restored in-place from online backup snapshot; reconnect sessions if auth state drifted",
	})
}

func verifyBackupFile(ctx context.Context, path string) error {
	readonly, err := store.OpenSQLiteReadonly(ctx, path)
	if err != nil {
		return err
	}
	defer readonly.Close()
	return readonly.IntegrityCheck(ctx)
}

func stringOrEmpty(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func marshalStrings(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func unmarshalStrings(value string) []string {
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return []string{}
	}
	return values
}
