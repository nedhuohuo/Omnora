package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/mountadmin"
	"omnora/internal/recovery"
	"omnora/internal/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// requireAdmin resolves the current session and confirms the account is a
// system administrator, writing an appropriate error response otherwise.
// TOTP is optional for administrators; only an active admin full session and
// cleared password-reset flag are required.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (identity.Session, bool) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return identity.Session{}, false
	}
	if session.Purpose != identity.SessionPurposeFull {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "TOTP enrollment sessions cannot access administrator actions")
		return identity.Session{}, false
	}
	var role string
	var passwordResetRequired int
	err = s.sqlDB().QueryRowContext(r.Context(), `
SELECT role, password_reset_required
FROM accounts
WHERE id = ? AND status = 'active'
`, session.AccountID).Scan(&role, &passwordResetRequired)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return identity.Session{}, false
	}
	if role != string(domain.AccountRoleAdmin) || passwordResetRequired != 0 {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can perform this action")
		return identity.Session{}, false
	}
	return session, true
}

// ---- Overview ----

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	db := s.sqlDB()
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	mountFilter := "purpose = 'common' AND status <> 'deleted'"
	if session.AccountID != initialAdminID {
		mountFilter += " AND governance = 'normal'"
	}

	accountsByStatus, err := countGroupedBy(r, db, "SELECT status, COUNT(1) FROM accounts GROUP BY status")
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	var commonMountCount int
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM mounts WHERE "+mountFilter).Scan(&commonMountCount); err != nil {
		writeDBError(w, r, err)
		return
	}
	mountsByHealth, err := countGroupedBy(r, db, "SELECT status, COUNT(1) FROM mounts WHERE "+mountFilter+" GROUP BY status")
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	jobsByStatus, err := s.countAdminJobs(r.Context(), session.AccountID)
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
	if err := db.QueryRowContext(r.Context(), "SELECT COUNT(1) FROM mounts WHERE "+mountFilter+" AND status = 'unavailable'").Scan(&unavailableMounts); err == nil && unavailableMounts > 0 {
		risks = append(risks, fmt.Sprintf("%d mount(s) require re-verification", unavailableMounts))
	}
	if latestBackup == nil {
		risks = append(risks, "no backups have been recorded yet")
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"accounts":       accountsByStatus,
		"commonMounts":   commonMountCount,
		"mountsByHealth": mountsByHealth,
		"jobsByStatus":   jobsByStatus,
		"routeGroups":    s.routeGroups(),
		"latestBackup":   latestBackup,
		"risks":          risks,
	})
}

func (s *Server) countAdminJobs(ctx context.Context, accountID string) (map[string]int, error) {
	rows, err := s.sqlDB().QueryContext(ctx, `SELECT status, payload_json FROM jobs`)
	if err != nil {
		return nil, err
	}
	type jobStatusEntry struct {
		status  string
		mountID string
	}
	entries := []jobStatusEntry{}
	for rows.Next() {
		var status, payload string
		if err := rows.Scan(&status, &payload); err != nil {
			_ = rows.Close()
			return nil, err
		}
		entries = append(entries, jobStatusEntry{
			status:  status,
			mountID: indexJobMountID(payload),
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, entry := range entries {
		if entry.mountID != "" {
			if _, err := s.mountAdmin.LoadMount(ctx, accountID, entry.mountID); err != nil {
				if errors.Is(err, mountadmin.ErrNotFound) {
					continue
				}
				return nil, err
			}
		}
		counts[entry.status]++
	}
	return counts, nil
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
	Protected    bool   `json:"protected"`
}

func (s *Server) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
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
		item.Protected = item.ID == initialAdminID
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAdminUser(w http.ResponseWriter, r *http.Request) {
	adminSession, ok := s.requireAdmin(w, r)
	if !ok {
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
	created, err := identity.New(s.sqlDB(), identity.Options{ManagedDir: s.cfg.Storage.ManagedDir}).CreateAccountSecure(r.Context(), identity.CreateAccountRequest{
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
		Role:        role,
	}, func(ctx context.Context, tx *sql.Tx, created identity.AccountWithPersonalDirectory) error {
		return s.recordAuditTx(ctx, tx, r, adminSession.AccountID, "admin_user_create", "account", created.Account.ID, "{}")
	})
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"user":              accountResponse(created.Account),
		"personalDirectory": created.PersonalDirectory,
	})
}

func (s *Server) setAdminUserEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	adminSession, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("userId")
	if !enabled && s.rejectInitialAdminMutation(w, r, userID, "the initial administrator must remain active") {
		return
	}
	status := "disabled"
	if enabled {
		status = "active"
	}
	svc := identity.New(s.sqlDB(), identity.Options{})
	if !enabled {
		err := svc.DisableAccountSecure(r.Context(), userID, func(ctx context.Context, tx *sql.Tx) error {
			return s.recordAuditTx(ctx, tx, r, adminSession.AccountID, "admin_user_disabled", "account", userID, "{}")
		})
		if err != nil {
			s.markAuditRiskIfNeeded(r.Context(), err)
			if errors.Is(err, identity.ErrInvalidCredential) {
				httpx.WriteError(w, r, http.StatusNotFound, "not_found", "user was not found")
			} else {
				writeDBError(w, r, err)
			}
			return
		}
	} else {
		tx, err := s.sqlDB().BeginTx(r.Context(), nil)
		if err != nil {
			writeDBError(w, r, err)
			return
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(r.Context(), `
UPDATE accounts SET status = 'active', updated_at = ?
WHERE id = ? AND status <> 'deleted'
`, nowRFC3339(), userID)
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
		if err := s.recordAuditTx(r.Context(), tx, r, adminSession.AccountID, "admin_user_active", "account", userID, "{}"); err != nil {
			s.markAuditRiskIfNeeded(r.Context(), err)
			writeDBError(w, r, err)
			return
		}
		if err := tx.Commit(); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": userID, "status": status})
}

func (s *Server) disableAdminUser(w http.ResponseWriter, r *http.Request) {
	s.setAdminUserEnabled(w, r, false)
}

func (s *Server) enableAdminUser(w http.ResponseWriter, r *http.Request) {
	s.setAdminUserEnabled(w, r, true)
}

func (s *Server) revokeAdminUserSessions(w http.ResponseWriter, r *http.Request) {
	adminSession, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("userId")
	if err := identity.New(s.sqlDB(), identity.Options{}).RevokeAllSessionsSecure(r.Context(), userID, "", func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, adminSession.AccountID, "admin_user_revoke_sessions", "account", userID, "{}")
	}); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Global shares & AI tokens ----

func (s *Server) listAdminShares(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	initialAdmin := 0
	if session.AccountID == initialAdminID {
		initialAdmin = 1
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT sh.id, sh.public_id, sh.mount_id,
       COALESCE(m.display_name, ''), COALESCE(m.purpose, ''), sh.relative_path,
       sh.allow_preview, sh.allow_download, sh.max_visits, sh.used_visits,
       sh.max_downloads, sh.used_downloads, sh.expires_at, COALESCE(sh.revoked_at, ''),
       COALESCE(a.email, ''), COALESCE(a.display_name, '')
FROM shares sh
JOIN mounts m ON m.id = sh.mount_id
JOIN accounts a ON a.id = sh.creator_account_id
WHERE ? = 1 OR m.governance = 'normal'
ORDER BY sh.created_at DESC, sh.id DESC
LIMIT ?
`, initialAdmin, parseIntDefault(r.URL.Query().Get("limit"), 200))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]shareRecordDTO, 0)
	for rows.Next() {
		var item shareRecordDTO
		var mountPurpose, expiresAt, revokedAt string
		var allowPreview, allowDownload int
		var maxVisits, maxDownloads sql.NullInt64
		if err := rows.Scan(&item.ID, &item.PublicID, &item.MountID,
			&item.MountName, &mountPurpose, &item.RelativePath, &allowPreview, &allowDownload,
			&maxVisits, &item.UsedVisits, &maxDownloads, &item.UsedDownloads, &expiresAt,
			&revokedAt, &item.CreatorEmail, &item.CreatorDisplayName); err != nil {
			writeDBError(w, r, err)
			return
		}
		item.Source = "common_mount"
		if mountPurpose == string(domain.MountPurposePersonalDefault) {
			item.Source = "personal"
			item.MountID = ""
			item.MountName = ""
		}
		item.AllowPreview = allowPreview == 1
		item.AllowDownload = allowDownload == 1
		if maxVisits.Valid {
			item.MaxVisits = &maxVisits.Int64
		}
		if maxDownloads.Valid {
			item.MaxDownloads = &maxDownloads.Int64
		}
		item.ExpiresAt = expiresAt
		item.RevokedAt = revokedAt
		item.Status = adminShareStatus(expiresAt, revokedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func adminShareStatus(expiresAt, revokedAt string) string {
	if revokedAt != "" {
		return "revoked"
	}
	if expiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, expiresAt); err == nil && !time.Now().UTC().Before(parsed) {
			return "expired"
		}
	}
	return "active"
}

func (s *Server) revokeAdminShare(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	initialAdmin := 0
	if session.AccountID == initialAdminID {
		initialAdmin = 1
	}
	shareID := r.PathValue("shareId")
	now := nowRFC3339()
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `
UPDATE shares
SET revoked_at = ?, updated_at = ?
WHERE id = ?
  AND revoked_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM mounts m
    WHERE m.id = shares.mount_id
      AND (? = 1 OR m.governance = 'normal')
  )
`, now, now, shareID, initialAdmin)
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
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "admin_share_revoke", "share", shareID, "{}"); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
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
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	tokenID := r.PathValue("tokenId")
	now := nowRFC3339()
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `
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
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "admin_ai_token_revoke", "ai_token", tokenID, "{}"); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Backups ----

type backupDTO struct {
	ID                   string `json:"id"`
	Status               string `json:"status"`
	Path                 string `json:"path,omitempty"`
	CreatedBy            string `json:"createdBy,omitempty"`
	CreatedByEmail       string `json:"createdByEmail,omitempty"`
	CreatedByDisplayName string `json:"createdByDisplayName,omitempty"`
	CreatedByLabel       string `json:"createdByLabel,omitempty"`
	CreatedAt            string `json:"createdAt"`
	CompletedAt          string `json:"completedAt,omitempty"`
	Notes                string `json:"notes,omitempty"`
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT b.id, b.status, COALESCE(b.path, ''), COALESCE(b.created_by, ''),
       COALESCE(a.email, ''), COALESCE(a.display_name, ''),
       b.created_at, COALESCE(b.completed_at, ''), b.notes
FROM backups b
LEFT JOIN accounts a ON a.id = b.created_by
ORDER BY b.created_at DESC
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
		if err := rows.Scan(&item.ID, &item.Status, &item.Path, &item.CreatedBy, &item.CreatedByEmail, &item.CreatedByDisplayName, &item.CreatedAt, &item.CompletedAt, &item.Notes); err != nil {
			writeDBError(w, r, err)
			return
		}
		item.CreatedByLabel = auditActorLabel(item.CreatedBy, item.CreatedByEmail, item.CreatedByDisplayName)
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// recoveryCoordinator returns a Coordinator configured with the durable
// offline recovery journal path for this instance's database. The journal
// lives outside the SQLite file itself, next to it, so it survives an
// offline recovery worker replacing that file wholesale with a backup
// snapshot; see cmd/omnora-recovery for the process that reads it back.
func (s *Server) recoveryCoordinator() *recovery.Coordinator {
	return recovery.NewCoordinator(s.sqlDB(), recovery.WithJournalPath(recovery.DefaultJournalPath(s.cfg.Database.Path)))
}

func (s *Server) recoveryStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	control, err := s.recoveryCoordinator().Control(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"state":               control.State,
		"ready":               control.Ready,
		"requestId":           control.RequestID,
		"reasonCode":          control.ReasonCode,
		"cleanupPending":      control.CleanupPending,
		"sourceSchemaVersion": control.SourceSchemaVersion,
		"requestedAt":         control.RequestedAt,
		"completedAt":         control.CompletedAt,
		"updatedAt":           control.UpdatedAt,
	})
}

func (s *Server) loadLatestBackup(r *http.Request) (*backupDTO, error) {
	var item backupDTO
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT b.id, b.status, COALESCE(b.path, ''), COALESCE(b.created_by, ''),
       COALESCE(a.email, ''), COALESCE(a.display_name, ''),
       b.created_at, COALESCE(b.completed_at, ''), b.notes
FROM backups b
LEFT JOIN accounts a ON a.id = b.created_by
ORDER BY b.created_at DESC
LIMIT 1
`).Scan(&item.ID, &item.Status, &item.Path, &item.CreatedBy, &item.CreatedByEmail, &item.CreatedByDisplayName, &item.CreatedAt, &item.CompletedAt, &item.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.CreatedByLabel = auditActorLabel(item.CreatedBy, item.CreatedByEmail, item.CreatedByDisplayName)
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
	backupSHA256 := ""
	var backupSizeBytes any
	var backupCanonicalPath any
	var backupSchemaVersion any

	if s.db == nil || strings.TrimSpace(s.cfg.Database.Path) == "" {
		status = "failed"
		notes = "database is not configured"
		completedAt = nil
	} else {
		backupsDir := filepath.Join(strings.TrimSpace(s.cfg.Storage.ManagedDir), "backups")
		artifact, err := recovery.NewBackupPublisher(s.db).Publish(r.Context(), backupsDir)
		if err != nil {
			status = "failed"
			notes = "online backup failed: " + err.Error()
			completedAt = nil
		} else {
			id = "bkp_" + artifact.ID
			backupPath = artifact.Path
			backupSHA256 = artifact.SHA256
			backupSizeBytes = artifact.SizeBytes
			backupSchemaVersion = artifact.SchemaVersion
			if canonical, err := filepath.Abs(filepath.Clean(artifact.Path)); err == nil {
				backupCanonicalPath = canonical
			} else {
				status = "failed"
				notes = "online backup failed: canonicalize backup artifact"
				backupPath = ""
				backupSHA256 = ""
				backupSizeBytes = nil
				backupSchemaVersion = nil
				completedAt = nil
			}
		}
	}

	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `
INSERT INTO backups(id, status, path, created_by, created_at, completed_at, notes, sha256, size_bytes, canonical_path, schema_version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, status, nullIfEmpty(backupPath), session.AccountID, now.Format(time.RFC3339Nano), completedAt, notes,
		nullIfEmpty(backupSHA256), backupSizeBytes, backupCanonicalPath, backupSchemaVersion)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "admin_backup_create", "backup", id, "{}"); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	createdByEmail, createdByDisplayName := s.accountDisplayFields(r, session.AccountID)
	httpx.WriteJSON(w, http.StatusCreated, backupDTO{
		ID: id, Status: status, Path: backupPath, CreatedBy: session.AccountID,
		CreatedByEmail: createdByEmail, CreatedByDisplayName: createdByDisplayName,
		CreatedByLabel: auditActorLabel(session.AccountID, createdByEmail, createdByDisplayName),
		CreatedAt:      now.Format(time.RFC3339Nano), CompletedAt: stringOrEmpty(completedAt), Notes: notes,
	})
}

func (s *Server) accountDisplayFields(r *http.Request, accountID string) (string, string) {
	if strings.TrimSpace(accountID) == "" {
		return "", ""
	}
	var email, displayName string
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT email, display_name
FROM accounts
WHERE id = ?
`, accountID).Scan(&email, &displayName)
	if err != nil {
		return "", ""
	}
	return email, displayName
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
	// An externally exposed server never replaces its live SQLite file from an
	// HTTP request. The coordinator records a durable, audited request and an
	// offline recovery worker performs staging, schema/integrity validation,
	// replacement, credential invalidation, and controlled bootstrap.
	if s.httpPolicy != nil {
		request, err := s.recoveryCoordinator().BeginRestore(r.Context(), recovery.BeginRestoreRequest{
			BackupID:       backupID,
			ActorAccountID: session.AccountID,
		})
		if err != nil {
			httpx.WriteError(w, r, http.StatusConflict, "restore_unavailable", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
			"status":    "restore_accepted",
			"requestId": request.ID,
			"backupId":  backupID,
			"state":     request.State,
			"notes":     "restore is staged for the offline recovery coordinator; this process is exiting so it never serves business routes against a database the recovery worker is about to replace",
		})
		// The 202 body is already written above; requesting shutdown here
		// only asks the process entry point to begin a graceful exit once
		// this handler returns and the response has flushed. It never
		// touches the live database itself. Run `omnora-recovery restore`
		// (see cmd/omnora-recovery) once this process has exited to perform
		// the actual offline replacement.
		s.RequestShutdown()
		return
	}

	// In-process test/CLI fixtures without an external trust policy retain the
	// legacy synchronous path. Production configuration cannot reach this
	// branch because ValidateBusinessExposure requires the trust boundary.
	if err := s.db.RestoreFrom(r.Context(), item.Path); err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "restore_failed", err.Error())
		return
	}
	if err := s.db.IntegrityCheck(r.Context()); err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "restore_integrity_failed", err.Error())
		return
	}
	_ = s.hydrateRouteGroups(r.Context())
	if !s.recordAuditMutation(w, r, "admin_backup_restore", "backup", backupID, fmt.Sprintf(`{"by":%q}`, session.AccountID)) {
		return
	}
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
