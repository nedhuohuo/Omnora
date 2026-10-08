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

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/mountid"
)

const teamMountName = "Team files"

var errManagedStorageUnavailable = errors.New("managed storage is unavailable")

type teamSpaceDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type teamSpaceCreateDTO struct {
	Space teamSpaceDTO `json:"space"`
	Mount mountDTO     `json:"mount"`
}

type teamSpaceMemberDTO struct {
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Permission  string `json:"permission"`
}

func (s *Server) createTeamSpace(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := normalizeSpaceName(req.Name)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}

	spaceID := "spc_" + httpx.NewRequestID()
	mountID := "mnt_" + httpx.NewRequestID()
	rootPath, err := teamSpaceRoot(s.cfg.Storage.ManagedDir, spaceID)
	if err != nil {
		writeTeamSpaceStorageUnavailable(w, r)
		return
	}
	existing, err := s.loadMountIdentities(r)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(rootPath), 0o750); err != nil {
		writeTeamSpaceStorageUnavailable(w, r)
		return
	}
	if err := os.Mkdir(rootPath, 0o750); err != nil {
		writeTeamSpaceStorageUnavailable(w, r)
		return
	}
	removeRoot := true
	defer func() {
		if removeRoot {
			_ = os.Remove(rootPath)
		}
	}()

	identity, err := mountid.VerifyCandidateRoot(rootPath, existing)
	if err != nil {
		writeTeamSpaceStorageUnavailable(w, r)
		return
	}
	if err := probeMountWritable(identity.Path); err != nil {
		writeTeamSpaceStorageUnavailable(w, r)
		return
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		writeDBError(w, r, err)
		return
	}

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
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json, created_at, updated_at)
VALUES (?, ?, ?, ?, 'managed', 'read_write', 0, 'active', ?, ?, ?)
`, mountID, spaceID, teamMountName, identity.Path, string(identityJSON), now, now); err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "team_space_create", "space", spaceID, "{}"); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	removeRoot = false
	httpx.WriteJSON(w, http.StatusCreated, teamSpaceCreateDTO{
		Space: teamSpaceDTO{ID: spaceID, Type: "shared", Name: name, Role: "manager"},
		Mount: mountDTO{
			ID: mountID, Name: teamMountName, Space: name, Kind: "managed", Mode: displayMountMode(domain.MountModeReadWrite),
			Index: displayIndex(0), Health: "active", Tone: "ok",
		},
	})
}

func teamSpaceRoot(managedRoot, spaceID string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(managedRoot))
	if root == "" || root == "." || root == string(filepath.Separator) || !filepath.IsAbs(root) {
		return "", errManagedStorageUnavailable
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || filepath.Base(spaceID) != spaceID {
		return "", errManagedStorageUnavailable
	}
	rootPath := filepath.Join(root, "team-spaces", spaceID)
	if rootPath == root || !strings.HasPrefix(rootPath, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: team space path is outside the configured root", errManagedStorageUnavailable)
	}
	return rootPath, nil
}

func writeTeamSpaceStorageUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusServiceUnavailable, "managed_storage_unavailable", "managed storage is unavailable")
}

func (s *Server) listTeamSpaceMembers(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	if !s.requireTeamSpaceManager(w, r, session.AccountID, spaceID) {
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT sm.account_id, a.email, a.display_name, sm.permission
FROM space_members sm
JOIN accounts a ON a.id = sm.account_id
WHERE sm.space_id = ? AND a.status = 'active'
ORDER BY a.display_name, a.id
`, spaceID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]teamSpaceMemberDTO, 0)
	for rows.Next() {
		var item teamSpaceMemberDTO
		if err := rows.Scan(&item.AccountID, &item.Email, &item.DisplayName, &item.Permission); err != nil {
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

func (s *Server) putTeamSpaceMember(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	accountRef := strings.TrimSpace(r.PathValue("accountRef"))
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
	permission := domain.SpacePermission(req.Permission)
	switch permission {
	case domain.SpacePermissionViewer, domain.SpacePermissionEditor, domain.SpacePermissionManager:
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "permission must be viewer, editor, or manager")
		return
	}
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	if !s.requireTeamSpaceManagerTx(w, r, tx, session.AccountID, spaceID) {
		return
	}
	member, ok := resolveActiveTeamSpaceMemberTx(w, r, tx, accountRef)
	if !ok {
		return
	}
	var currentPermission string
	err = tx.QueryRowContext(r.Context(), `
SELECT permission FROM space_members WHERE space_id = ? AND account_id = ?
`, spaceID, member.AccountID).Scan(&currentPermission)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeDBError(w, r, err)
		return
	}
	if currentPermission == string(domain.SpacePermissionManager) && permission != domain.SpacePermissionManager {
		if !s.requireAnotherTeamSpaceManager(w, r, tx, spaceID) {
			return
		}
	}
	now := nowRFC3339()
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO space_members(space_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(space_id, account_id) DO UPDATE SET permission = excluded.permission, updated_at = excluded.updated_at
`, spaceID, member.AccountID, string(permission), now, now); err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "team_space_member_set", "space", spaceID, fmt.Sprintf(`{"accountId":%q,"permission":%q}`, member.AccountID, permission)); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	member.Permission = string(permission)
	httpx.WriteJSON(w, http.StatusOK, member)
}

func (s *Server) deleteTeamSpaceMember(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	accountID := strings.TrimSpace(r.PathValue("accountRef"))
	if accountID == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "account id is required")
		return
	}
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	if !s.requireTeamSpaceManagerTx(w, r, tx, session.AccountID, spaceID) {
		return
	}
	var permission string
	err = tx.QueryRowContext(r.Context(), `
SELECT permission FROM space_members WHERE space_id = ? AND account_id = ?
`, spaceID, accountID).Scan(&permission)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "membership was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if permission == string(domain.SpacePermissionManager) && !s.requireAnotherTeamSpaceManager(w, r, tx, spaceID) {
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM space_members WHERE space_id = ? AND account_id = ?`, spaceID, accountID); err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "team_space_member_remove", "space", spaceID, fmt.Sprintf(`{"accountId":%q}`, accountID)); err != nil {
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

func (s *Server) requireTeamSpaceManager(w http.ResponseWriter, r *http.Request, accountID, spaceID string) bool {
	return s.requireTeamSpaceManagerQuery(w, r, s.sqlDB(), accountID, spaceID)
}

func (s *Server) requireTeamSpaceManagerTx(w http.ResponseWriter, r *http.Request, tx *sql.Tx, accountID, spaceID string) bool {
	return s.requireTeamSpaceManagerQuery(w, r, tx, accountID, spaceID)
}

type teamSpaceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Server) requireTeamSpaceManagerQuery(w http.ResponseWriter, r *http.Request, queryer teamSpaceQueryer, accountID, spaceID string) bool {
	var kind string
	err := queryer.QueryRowContext(r.Context(), `SELECT kind FROM spaces WHERE id = ? AND status = 'active'`, spaceID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "space was not found")
		return false
	}
	if err != nil {
		writeDBError(w, r, err)
		return false
	}
	if kind != "shared" {
		httpx.WriteError(w, r, http.StatusConflict, "personal_space_protected", "personal spaces cannot be managed as team spaces")
		return false
	}
	var permission string
	err = queryer.QueryRowContext(r.Context(), `
SELECT sm.permission
FROM space_members sm
JOIN accounts a ON a.id = sm.account_id
WHERE sm.space_id = ? AND sm.account_id = ? AND a.status = 'active'
`, spaceID, accountID).Scan(&permission)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "team space manager permission is required")
		return false
	}
	if err != nil {
		writeDBError(w, r, err)
		return false
	}
	if permission != string(domain.SpacePermissionManager) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "team space manager permission is required")
		return false
	}
	return true
}

func resolveActiveTeamSpaceMemberTx(w http.ResponseWriter, r *http.Request, tx *sql.Tx, accountRef string) (teamSpaceMemberDTO, bool) {
	var member teamSpaceMemberDTO
	err := tx.QueryRowContext(r.Context(), `
SELECT id, email, display_name
FROM accounts
WHERE status = 'active' AND (id = ? OR lower(email) = lower(?))
`, accountRef, accountRef).Scan(&member.AccountID, &member.Email, &member.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "active account was not found")
		return teamSpaceMemberDTO{}, false
	}
	if err != nil {
		writeDBError(w, r, err)
		return teamSpaceMemberDTO{}, false
	}
	return member, true
}

func (s *Server) requireAnotherTeamSpaceManager(w http.ResponseWriter, r *http.Request, tx *sql.Tx, spaceID string) bool {
	var managerCount int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM space_members WHERE space_id = ? AND permission = 'manager'`, spaceID).Scan(&managerCount); err != nil {
		writeDBError(w, r, err)
		return false
	}
	if managerCount <= 1 {
		httpx.WriteError(w, r, http.StatusConflict, "last_manager_required", "a team space must retain at least one manager")
		return false
	}
	return true
}
