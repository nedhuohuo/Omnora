package server

import (
	"errors"
	"net/http"
	"strings"

	"omnora/internal/domain"
	"omnora/internal/foldercollab"
	"omnora/internal/httpx"
)

func (s *Server) folderCollaborationService() *foldercollab.Service {
	return foldercollab.New(s.sqlDB(), s.cfg.Storage.ManagedDir)
}

func (s *Server) listMemberCollaborationsV2(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	direction := foldercollab.Direction(strings.TrimSpace(r.URL.Query().Get("direction")))
	items, err := s.folderCollaborationService().List(r.Context(), session.AccountID, direction)
	if err != nil {
		writeFolderCollaborationError(w, r, err)
		return
	}
	writeFolderCollaborationItems(w, items)
}

func (s *Server) createMemberCollaboration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RecipientID      string                   `json:"recipientId"`
		RootRelativePath string                   `json:"rootRelativePath"`
		Permission       domain.ContentPermission `json:"permission"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	item, err := s.folderCollaborationService().Create(r.Context(), session.AccountID, foldercollab.CreateRequest{
		RecipientID: req.RecipientID, RootRelativePath: req.RootRelativePath, Permission: req.Permission,
	})
	if err != nil {
		writeFolderCollaborationError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, collaborationResponse(item))
}

func (s *Server) updateMemberCollaboration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Permission domain.ContentPermission `json:"permission"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	item, err := s.folderCollaborationService().Update(r.Context(), session.AccountID, r.PathValue("collaborationId"), foldercollab.UpdateRequest{Permission: req.Permission})
	if err != nil {
		writeFolderCollaborationError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, collaborationResponse(item))
}

func (s *Server) revokeMemberCollaboration(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if err := s.folderCollaborationService().Revoke(r.Context(), session.AccountID, r.PathValue("collaborationId")); err != nil {
		writeFolderCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeFolderCollaborationItems(w http.ResponseWriter, items []foldercollab.Item) {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, collaborationResponse(item))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": result})
}

func collaborationResponse(item foldercollab.Item) map[string]any {
	return map[string]any{
		"id": item.ID, "source": "collaboration", "collaborationId": item.ID,
		"rootRelativePath": item.RootRelativePath, "folderName": item.FolderName,
		"displayName": item.FolderName, "path": item.RootRelativePath,
		"ownerName": item.OwnerDisplayName, "recipientName": item.RecipientDisplayName,
		"permission": item.Permission, "status": item.Status,
	}
}

func writeFolderCollaborationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, foldercollab.ErrInvalidInput):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "collaboration input is invalid")
	case errors.Is(err, foldercollab.ErrOverlappingGrant):
		httpx.WriteError(w, r, http.StatusConflict, "overlapping_grant", "the recipient already has an overlapping collaboration")
	case errors.Is(err, foldercollab.ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only the personal directory owner can manage this collaboration")
	case errors.Is(err, foldercollab.ErrNotFound), errors.Is(err, foldercollab.ErrUnavailable):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "collaboration was not found")
	default:
		writeDBError(w, r, err)
	}
}
