package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"omnora/internal/access"
	"omnora/internal/httpx"
	"omnora/internal/membershare"
)

type shareRecordDTO struct {
	ID                 string `json:"id"`
	PublicID           string `json:"publicId"`
	Fragment           string `json:"fragment,omitempty"`
	Source             string `json:"source"`
	MountID            string `json:"mountId,omitempty"`
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

// listShares returns shares visible to the current account under account-level
// content and share governance rules.
func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	items, err := s.memberShares.List(r.Context(), access.Subject{AccountID: session.AccountID}, membershare.ListFilter{Limit: parseIntDefault(r.URL.Query().Get("limit"), 100)})
	if err != nil {
		writeMemberShareError(w, r, err)
		return
	}
	legacy := make([]shareRecordDTO, 0, len(items))
	for _, item := range items {
		legacy = append(legacy, memberShareDTO(item))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": legacy})
}

// revokeShare revokes a share if the current account is allowed by the
// account-level share governance service.
func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	shareID := r.PathValue("shareId")
	if err := s.memberShares.RevokeSecure(r.Context(), access.Subject{AccountID: session.AccountID}, shareID, func(ctx context.Context, tx *sql.Tx, shareID string) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "share_revoke", "share", shareID, "{}")
	}); err != nil {
		writeMemberShareError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func memberShareDTO(item membershare.Share) shareRecordDTO {
	status := item.Status
	// The legacy REST contract exposes only active/expired/revoked. MCP's
	// richer member-share model retains exhausted for protocol clients.
	if status == "exhausted" {
		status = "active"
	}
	result := shareRecordDTO{
		ID: item.ID, PublicID: item.PublicID,
		Source: string(item.Source), MountID: item.MountID, MountName: item.MountName, RelativePath: item.RelativePath,
		CreatorEmail: item.CreatorEmail, CreatorDisplayName: item.CreatorDisplayName,
		AllowPreview: item.AllowPreview, AllowDownload: item.AllowDownload,
		MaxVisits: item.MaxVisits, UsedVisits: int(item.UsedVisits), MaxDownloads: item.MaxDownloads,
		UsedDownloads: int(item.UsedDownloads), ExpiresAt: item.ExpiresAt.Format(time.RFC3339Nano), Status: status,
	}
	if item.RevokedAt != nil {
		result.RevokedAt = item.RevokedAt.Format(time.RFC3339Nano)
	}
	return result
}

func writeMemberShareError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, membershare.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "share was not found")
	case errors.Is(err, membershare.ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "the account is not allowed to manage this share")
	case errors.Is(err, access.ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "creating shares requires editor permission")
	case errors.Is(err, membershare.ErrUnauthorized):
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
	case errors.Is(err, access.ErrBoundaryViolation):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "path is invalid")
	case errors.Is(err, membershare.ErrInvalidInput):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, access.ErrMountIdentityUnverifiable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
	case errors.Is(err, access.ErrMountUnavailable):
		httpx.WriteError(w, r, http.StatusConflict, "mount_unavailable", "mount is unavailable and must be re-verified by an administrator")
	case errors.Is(err, sql.ErrNoRows):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
	default:
		writeDBError(w, r, err)
	}
}
