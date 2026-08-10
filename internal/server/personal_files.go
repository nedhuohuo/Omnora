package server

import (
	"errors"
	"net/http"
	"strings"

	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/personalstorage"
)

func (s *Server) listMemberContentSources(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if s.personalStorage == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "personal_storage_unavailable", "personal storage is unavailable")
		return
	}
	sources, err := s.personalStorage.ContentSources(r.Context(), session.AccountID)
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sources)
}

func (s *Server) listMemberFileChildren(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	query := r.URL.Query()
	for _, forbidden := range []string{"accountId", "defaultMountId", "spaceId"} {
		if _, present := query[forbidden]; present {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "member file locator contains a forbidden identity field")
			return
		}
	}
	if s.personalStorage == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "personal_storage_unavailable", "personal storage is unavailable")
		return
	}
	var listing files.DirectoryListing
	switch strings.TrimSpace(query.Get("source")) {
	case "personal":
		if query.Get("mountId") != "" || query.Get("collaborationId") != "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "personal file locator is invalid")
			return
		}
		listing, err = s.personalStorage.ListPersonal(r.Context(), session.AccountID, query.Get("path"))
	case "common_mount":
		if strings.TrimSpace(query.Get("mountId")) == "" || query.Get("collaborationId") != "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "common mount locator is invalid")
			return
		}
		listing, err = s.personalStorage.ListCommon(r.Context(), session.AccountID, query.Get("mountId"), query.Get("path"))
	case "collaboration":
		if strings.TrimSpace(query.Get("collaborationId")) == "" || query.Get("mountId") != "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "collaboration locator is invalid")
			return
		}
		listing, err = s.personalStorage.ListCollaboration(r.Context(), session.AccountID, query.Get("collaborationId"), query.Get("path"))
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "member file locator is invalid")
		return
	}
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, listing)
}

func (s *Server) listMemberCollaborations(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	if direction != "incoming" && direction != "outgoing" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_direction", "collaboration direction must be incoming or outgoing")
		return
	}
	accountColumn := "collaboration.recipient_account_id"
	if direction == "outgoing" {
		accountColumn = "collaboration.owner_account_id"
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT collaboration.id, collaboration.root_relative_path,
       owner.display_name, recipient.display_name, collaboration.permission
FROM folder_collaborations AS collaboration
JOIN accounts AS owner ON owner.id = collaboration.owner_account_id
JOIN accounts AS recipient ON recipient.id = collaboration.recipient_account_id
JOIN personal_directories AS directory ON directory.account_id = collaboration.owner_account_id
WHERE `+accountColumn+` = ?
  AND collaboration.revoked_at IS NULL
  AND owner.status = 'active'
  AND recipient.status = 'active'
  AND directory.state = 'ready'
ORDER BY collaboration.created_at DESC, collaboration.id`, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	type collaborationItem struct {
		ID            string `json:"id"`
		DisplayName   string `json:"displayName"`
		Path          string `json:"path"`
		OwnerName     string `json:"ownerName"`
		RecipientName string `json:"recipientName"`
		Permission    string `json:"permission"`
		Status        string `json:"status"`
	}
	items := []collaborationItem{}
	for rows.Next() {
		var item collaborationItem
		if err := rows.Scan(&item.ID, &item.Path, &item.OwnerName, &item.RecipientName, &item.Permission); err != nil {
			writeDBError(w, r, err)
			return
		}
		item.DisplayName = item.Path
		if item.DisplayName == "" || item.DisplayName == "." {
			item.DisplayName = "Personal files"
		}
		item.Status = "active"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writePersonalStorageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, personalstorage.ErrInvalidInput):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "personal file locator is invalid")
	case errors.Is(err, personalstorage.ErrUnavailable):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "personal files are unavailable")
	default:
		writeDBError(w, r, err)
	}
}
