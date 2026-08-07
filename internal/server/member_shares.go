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

// listShares returns shares created by the current account, plus shares in
// any space where the current account is currently a manager.
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

// revokeShare revokes a share if the current account is the creator or a
// manager of the space the share belongs to.
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
		ID: item.ID, PublicID: item.PublicID, SpaceID: item.SpaceID, SpaceName: item.SpaceName,
		MountID: item.MountID, MountName: item.MountName, RelativePath: item.RelativePath,
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
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only the creator or a space manager can revoke this share")
	case errors.Is(err, access.ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "creating shares requires manager permission")
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
	var fragmentSecret string
	var allowPreview, allowDownload int
	var maxVisits, maxDownloads sql.NullInt64
	if err := rows.Scan(
		&item.ID, &item.PublicID, &fragmentSecret, &item.SpaceID, &item.MountID, &item.RelativePath,
		&allowPreview, &allowDownload, &maxVisits, &item.UsedVisits,
		&maxDownloads, &item.UsedDownloads, &item.ExpiresAt, &item.RevokedAt,
		&item.SpaceName, &item.MountName, &item.CreatorEmail, &item.CreatorDisplayName,
	); err != nil {
		return shareRecordDTO{}, err
	}
	if fragmentSecret != "" {
		item.Fragment = item.PublicID + "." + fragmentSecret
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
