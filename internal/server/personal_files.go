package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strings"

	"omnora/internal/access"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
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

type memberLocatorRequest struct {
	Source  string `json:"source"`
	MountID string `json:"mountId"`
	Path    string `json:"path"`
}

func memberMutationLocator(req memberLocatorRequest) (access.Locator, error) {
	switch contentref.Source(strings.TrimSpace(req.Source)) {
	case contentref.SourcePersonal:
		if strings.TrimSpace(req.MountID) != "" {
			return access.Locator{}, personalstorage.ErrInvalidInput
		}
		return access.Locator{Source: contentref.SourcePersonal, Path: req.Path}, nil
	case contentref.SourceCommonMount:
		if strings.TrimSpace(req.MountID) == "" {
			return access.Locator{}, personalstorage.ErrInvalidInput
		}
		return access.Locator{Source: contentref.SourceCommonMount, MountID: req.MountID, Path: req.Path}, nil
	default:
		return access.Locator{}, personalstorage.ErrInvalidInput
	}
}

func memberLocatorFromQuery(r *http.Request) (access.Locator, error) {
	query := r.URL.Query()
	for _, forbidden := range []string{"accountId", "defaultMountId", "spaceId", "collaborationId"} {
		if _, present := query[forbidden]; present {
			return access.Locator{}, personalstorage.ErrInvalidInput
		}
	}
	return memberMutationLocator(memberLocatorRequest{Source: query.Get("source"), MountID: query.Get("mountId"), Path: query.Get("path")})
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

func (s *Server) memberMutationSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return "", false
	}
	return session.AccountID, true
}

func (s *Server) createMemberDirectory(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.memberMutationSession(w, r)
	if !ok {
		return
	}
	var req struct {
		memberLocatorRequest
		ParentPath string `json:"parentPath"`
		Name       string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Path = req.ParentPath
	locator, err := memberMutationLocator(req.memberLocatorRequest)
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	result, err := s.memberFiles.CreateDirectory(r.Context(), access.Subject{AccountID: accountID}, locator, req.Name)
	if err != nil {
		writeMemberFilesError(w, r, err, "creating a directory requires editor permission")
		return
	}
	if !s.recordAuditMutation(w, r, "directory_create", "directory", result.RelativePath, "{}") {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (s *Server) renameMemberObject(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.memberMutationSession(w, r)
	if !ok {
		return
	}
	var req struct {
		memberLocatorRequest
		ToName string `json:"toName"`
		ToPath string `json:"toPath"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	locator, err := memberMutationLocator(req.memberLocatorRequest)
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	target, err := resolveRenameTarget(locator.Path, req.ToName, req.ToPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	result, err := s.memberFiles.RenameSecure(r.Context(), access.Subject{AccountID: accountID}, locator, target, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, accountID, "object_rename", "file_object", target, "{}")
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "renaming requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) memberCopyMove(w http.ResponseWriter, r *http.Request, move bool) {
	accountID, ok := s.memberMutationSession(w, r)
	if !ok {
		return
	}
	var req struct {
		memberLocatorRequest
		Destination memberLocatorRequest `json:"destination"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	source, err := memberMutationLocator(req.memberLocatorRequest)
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	destination, err := memberMutationLocator(req.Destination)
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	var result memberfiles.MutationResult
	if move {
		result, err = s.memberFiles.MoveSecure(r.Context(), access.Subject{AccountID: accountID}, source, destination, func(ctx context.Context, tx *sql.Tx) error {
			return s.recordAuditTx(ctx, tx, r, accountID, "object_move", "file_object", source.Path, "{}")
		})
	} else {
		result, err = s.memberFiles.Copy(r.Context(), access.Subject{AccountID: accountID}, source, destination)
		if err == nil && !s.recordAuditMutation(w, r, "object_copy", "file_object", result.RelativePath, "{}") {
			return
		}
	}
	if err != nil {
		writeMemberFilesError(w, r, err, "file operation requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) copyMemberObject(w http.ResponseWriter, r *http.Request) {
	s.memberCopyMove(w, r, false)
}
func (s *Server) moveMemberObject(w http.ResponseWriter, r *http.Request) {
	s.memberCopyMove(w, r, true)
}

func (s *Server) deleteMemberObject(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.memberMutationSession(w, r)
	if !ok {
		return
	}
	locator, err := memberLocatorFromQuery(r)
	if err != nil || strings.TrimSpace(locator.Path) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "member file locator is invalid")
		return
	}
	permanent := r.URL.Query().Get("permanent") == "true"
	if permanent {
		_, err = s.memberFiles.DeletePermanentlySecure(r.Context(), access.Subject{AccountID: accountID}, locator, func(ctx context.Context, tx *sql.Tx) error {
			return s.recordAuditTx(ctx, tx, r, accountID, "object_delete", "file_object", locator.Path, "{}")
		})
	} else {
		_, err = s.memberFiles.TrashSecure(r.Context(), access.Subject{AccountID: accountID}, locator, func(ctx context.Context, tx *sql.Tx) error {
			return s.recordAuditTx(ctx, tx, r, accountID, "object_trash", "file_object", locator.Path, "{}")
		})
	}
	if err != nil {
		writeMemberFilesError(w, r, err, "deleting requires editor permission")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createMemberUpload(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.memberMutationSession(w, r)
	if !ok {
		return
	}
	var req struct {
		memberLocatorRequest
		ParentPath string `json:"parentPath"`
		FileName   string `json:"fileName"`
		Size       int64  `json:"size"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Path = pathJoinForUpload(req.ParentPath, req.FileName)
	locator, err := memberMutationLocator(req.memberLocatorRequest)
	if err != nil {
		writePersonalStorageError(w, r, err)
		return
	}
	result, err := s.memberFiles.PrepareUpload(r.Context(), access.Subject{AccountID: accountID}, memberfiles.UploadRequest{Locator: locator, ExpectedSize: req.Size})
	if err != nil {
		writeMemberFilesError(w, r, err, "upload requires editor permission")
		return
	}
	if !s.recordAuditMutation(w, r, "upload_create", "upload", result.ID, "{}") {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (s *Server) downloadMemberObject(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.memberMutationSession(w, r)
	if !ok {
		return
	}
	locator, err := memberLocatorFromQuery(r)
	if err != nil || strings.TrimSpace(locator.Path) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "member file locator is invalid")
		return
	}
	mount, err := s.guard.Authorize(r.Context(), access.CheckRequest{Subject: access.Subject{AccountID: accountID}, Locator: locator, RequiredPermission: domain.ContentPermissionViewer})
	if err != nil {
		writeMemberFilesError(w, r, err, "downloading requires access")
		return
	}
	file, info, err := files.NewService().OpenFile(files.Mount{Root: mount.Root, Mode: mount.Mode}, mount.RelativePath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "file was not found")
		return
	}
	defer file.Close()
	if r.URL.Query().Get("inline") == "true" {
		w.Header().Set("Content-Disposition", "inline")
	}
	http.ServeContent(w, r, path.Base(mount.RelativePath), info.ModTime(), file)
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
