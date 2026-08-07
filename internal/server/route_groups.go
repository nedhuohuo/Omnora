package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/httpx"
)

func (s *Server) listAdminRouteGroups(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can list route groups")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": s.routeGroups()})
}

func (s *Server) updateAdminRouteGroup(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can update route groups")
		return
	}

	group := domain.RouteGroup(strings.TrimSpace(r.PathValue("groupId")))
	if !group.Valid() {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "route group was not found")
		return
	}

	var req struct {
		Exposed *bool `json:"exposed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Exposed == nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "exposed is required")
		return
	}

	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	if err := s.persistRouteGroupTx(r.Context(), tx, group, *req.Exposed, session.AccountID); err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "route_group_update", "route_group", string(group), fmt.Sprintf(`{"exposed":%t}`, *req.Exposed)); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	s.setRouteEnabled(group, *req.Exposed)
	httpx.WriteJSON(w, http.StatusOK, s.routeGroupDTO(group))
}

func (s *Server) hydrateRouteGroups(ctx context.Context) error {
	db := s.sqlDB()
	if db == nil {
		return nil
	}

	rows, err := db.QueryContext(ctx, `SELECT name, enabled FROM route_groups`)
	if err != nil {
		return err
	}
	defer rows.Close()

	loaded := make(map[domain.RouteGroup]bool, len(domain.AllRouteGroups))
	for rows.Next() {
		var name string
		var enabled int
		if err := rows.Scan(&name, &enabled); err != nil {
			return err
		}
		group := domain.RouteGroup(name)
		if group.Valid() {
			loaded[group] = enabled == 1
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.routesMu.Lock()
	defer s.routesMu.Unlock()

	for group, enabled := range loaded {
		s.cfg.Routes.Set(group, enabled)
	}
	for group, enabled := range s.cfg.RouteEnvOverrides {
		s.cfg.Routes.Set(group, enabled)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, group := range domain.AllRouteGroups {
		enabled := 0
		if s.cfg.Routes.Enabled(group) {
			enabled = 1
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO route_groups(name, enabled, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
	enabled = excluded.enabled,
	updated_at = excluded.updated_at
`, string(group), enabled, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) persistRouteGroup(ctx context.Context, group domain.RouteGroup, exposed bool, updatedBy string) error {
	db := s.sqlDB()
	if db == nil {
		return sql.ErrConnDone
	}
	enabled := 0
	if exposed {
		enabled = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.ExecContext(ctx, `
INSERT INTO route_groups(name, enabled, updated_by, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
	enabled = excluded.enabled,
	updated_by = excluded.updated_by,
	updated_at = excluded.updated_at
`, string(group), enabled, nullIfEmpty(updatedBy), now)
	if err != nil {
		return err
	}
	if _, err := result.RowsAffected(); err != nil {
		return err
	}
	return nil
}

func (s *Server) persistRouteGroupTx(ctx context.Context, tx *sql.Tx, group domain.RouteGroup, exposed bool, updatedBy string) error {
	if tx == nil {
		return errors.New("route group transaction is nil")
	}
	enabled := 0
	if exposed {
		enabled = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
INSERT INTO route_groups(name, enabled, updated_by, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
	enabled = excluded.enabled,
	updated_by = excluded.updated_by,
	updated_at = excluded.updated_at
`, string(group), enabled, nullIfEmpty(updatedBy), now)
	if err != nil {
		return err
	}
	_, err = result.RowsAffected()
	return err
}

func (s *Server) routeGroupDTO(group domain.RouteGroup) routeGroupDTO {
	labels := routeGroupLabels()
	exposed := s.routeEnabled(group)
	tone := "muted"
	risk := "closed"
	entry := "disabled"
	if exposed {
		tone = "ok"
		risk = "enabled by explicit route group"
		entry = EntryHTTP
	}
	return routeGroupDTO{
		ID: string(group), Label: labels[group], Exposed: exposed, Entry: entry, Risk: risk, Tone: tone,
	}
}

func routeGroupLabels() map[domain.RouteGroup]string {
	return map[domain.RouteGroup]string{
		domain.RouteGroupMemberWeb: "Member Web",
		domain.RouteGroupAdminWeb:  "Admin Web",
		domain.RouteGroupShare:     "Share",
		domain.RouteGroupREST:      "REST",
		domain.RouteGroupMCP:       "MCP",
		domain.RouteGroupOpenAPI:   "OpenAPI",
	}
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
