package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/mountadmin"
)

type adminMountDTO struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	RootPath     string `json:"rootPath"`
	Governance   string `json:"governance"`
	Mode         string `json:"mode"`
	IndexEnabled bool   `json:"indexEnabled"`
	ShareEnabled bool   `json:"shareEnabled"`
	Status       string `json:"status"`
	GrantCount   int    `json:"grantCount"`
}

type adminMountGrantDTO struct {
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Permission  string `json:"permission"`
}

func adminMountDTOFrom(item mountadmin.Mount) adminMountDTO {
	return adminMountDTO{
		ID: item.ID, DisplayName: item.DisplayName, RootPath: item.RootPath,
		Governance: string(item.Governance), Mode: string(item.Mode),
		IndexEnabled: item.IndexEnabled, ShareEnabled: item.ShareEnabled,
		Status: item.Status, GrantCount: item.GrantCount,
	}
}

func adminMountGrantDTOFrom(item mountadmin.Grant) adminMountGrantDTO {
	return adminMountGrantDTO{AccountID: item.AccountID, Email: item.Email, DisplayName: item.DisplayName, Permission: string(item.Permission)}
}

func (s *Server) writeMountAdminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, mountadmin.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
	case errors.Is(err, mountadmin.ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can manage mounts")
	case errors.Is(err, mountadmin.ErrRestrictedGovernance):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "restricted mounts require the initial administrator")
	case errors.Is(err, mountadmin.ErrRootNotAllowed):
		httpx.WriteError(w, r, http.StatusForbidden, "mount_root_not_allowed", "mount root is outside the external storage root")
	case errors.Is(err, mountadmin.ErrMountUnavailable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_unavailable", "mount is unavailable")
	case errors.Is(err, mountadmin.ErrMountConflict):
		httpx.WriteError(w, r, http.StatusConflict, "mount_conflict", "mount conflicts with an existing mount")
	case errors.Is(err, mountadmin.ErrIdentityUnverifiable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
	case errors.Is(err, mountadmin.ErrNotWritable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_not_writable", "mount root is not writable")
	case errors.Is(err, mountadmin.ErrConfirmationRequired):
		httpx.WriteError(w, r, http.StatusConflict, "confirmation_required", "displayName must match the current mount name")
	case errors.Is(err, mountadmin.ErrInvalidInput):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		writeDBError(w, r, err)
	}
}

func (s *Server) mountAuditWriter(r *http.Request, actorID string) mountadmin.AuditWriter {
	return func(ctx context.Context, tx *sql.Tx, event mountadmin.AuditEvent) error {
		return s.recordAuditTx(ctx, tx, r, actorID, event.Action, "mount", event.TargetID, event.Metadata)
	}
}

func (s *Server) adminListMounts(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	items, err := s.mountAdmin.ListMounts(r.Context(), session.AccountID)
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	response := make([]adminMountDTO, 0, len(items))
	for _, item := range items {
		response = append(response, adminMountDTOFrom(item))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": response})
}

func (s *Server) adminCreateMount(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		DisplayName  string `json:"displayName"`
		RootPath     string `json:"rootPath"`
		Governance   string `json:"governance"`
		Mode         string `json:"mode"`
		IndexEnabled bool   `json:"indexEnabled"`
		Grants       []struct {
			AccountID  string `json:"accountId"`
			Permission string `json:"permission"`
		} `json:"grants"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	grants := make([]mountadmin.GrantInput, 0, len(req.Grants))
	for _, grant := range req.Grants {
		grants = append(grants, mountadmin.GrantInput{AccountID: grant.AccountID, Permission: domain.ContentPermission(grant.Permission)})
	}
	item, err := s.mountAdmin.CreateMount(r.Context(), session.AccountID, mountadmin.CreateRequest{
		ID: "mnt_" + httpx.NewRequestID(), DisplayName: req.DisplayName, RootPath: req.RootPath,
		Governance: domain.MountGovernance(req.Governance), Mode: domain.MountMode(req.Mode),
		IndexEnabled: req.IndexEnabled, Grants: grants,
	}, s.mountAuditWriter(r, session.AccountID))
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, adminMountDTOFrom(item))
}

func (s *Server) adminUpdateMount(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		DisplayName  *string `json:"displayName"`
		RootPath     *string `json:"rootPath"`
		Governance   *string `json:"governance"`
		Mode         *string `json:"mode"`
		IndexEnabled *bool   `json:"indexEnabled"`
		ShareEnabled *bool   `json:"shareEnabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.RootPath != nil || req.Governance != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "rootPath and governance are immutable")
		return
	}
	var mode *domain.MountMode
	if req.Mode != nil {
		value := domain.MountMode(*req.Mode)
		mode = &value
	}
	item, err := s.mountAdmin.UpdateMount(r.Context(), session.AccountID, r.PathValue("mountId"), mountadmin.UpdateRequest{
		DisplayName: req.DisplayName, Mode: mode, IndexEnabled: req.IndexEnabled, ShareEnabled: req.ShareEnabled,
	}, s.mountAuditWriter(r, session.AccountID))
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, adminMountDTOFrom(item))
}

func (s *Server) adminDeleteMount(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"displayName"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.mountAdmin.DeleteMount(r.Context(), session.AccountID, r.PathValue("mountId"), req.DisplayName, s.mountAuditWriter(r, session.AccountID))
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) adminReverifyMount(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	item, err := s.mountAdmin.ReverifyMount(r.Context(), session.AccountID, r.PathValue("mountId"), s.mountAuditWriter(r, session.AccountID))
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, adminMountDTOFrom(item))
}

func (s *Server) adminListMountGrants(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	items, err := s.mountAdmin.ListGrants(r.Context(), session.AccountID, r.PathValue("mountId"))
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	response := make([]adminMountGrantDTO, 0, len(items))
	for _, item := range items {
		response = append(response, adminMountGrantDTOFrom(item))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": response})
}

func (s *Server) adminPutMountGrant(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		Permission string `json:"permission"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := s.mountAdmin.PutGrant(r.Context(), session.AccountID, r.PathValue("mountId"), r.PathValue("accountId"), domain.ContentPermission(req.Permission), s.mountAuditWriter(r, session.AccountID))
	if err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, adminMountGrantDTOFrom(item))
}

func (s *Server) adminDeleteMountGrant(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if err := s.mountAdmin.DeleteGrant(r.Context(), session.AccountID, r.PathValue("mountId"), r.PathValue("accountId"), s.mountAuditWriter(r, session.AccountID)); err != nil {
		s.writeMountAdminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
