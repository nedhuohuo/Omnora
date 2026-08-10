package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
	"omnora/internal/transfer"
)

func memberLocatorFromQuery(r *http.Request) (access.Locator, error) {
	query := r.URL.Query()
	for _, forbidden := range []string{"accountId", "defaultMountId", "spaceId", "space_id"} {
		if _, present := query[forbidden]; present {
			return access.Locator{}, fmt.Errorf("%s is not a valid content locator field", forbidden)
		}
	}
	locator := access.Locator{Source: contentref.Source(strings.TrimSpace(query.Get("source"))), Path: query.Get("path")}
	switch locator.Source {
	case contentref.SourcePersonal:
		if query.Get("mountId") != "" || query.Get("collaborationId") != "" {
			return access.Locator{}, errors.New("personal locator cannot include a mount or collaboration")
		}
	case contentref.SourceCommonMount:
		locator.MountID = strings.TrimSpace(query.Get("mountId"))
		if locator.MountID == "" || query.Get("collaborationId") != "" {
			return access.Locator{}, errors.New("common mount locator requires mountId")
		}
	default:
		return access.Locator{}, errors.New("source must be personal or common_mount")
	}
	return locator, nil
}

func memberLocatorFromJSON(source string, mountID string, collaborationID string, path string) (access.Locator, error) {
	locator := access.Locator{Source: contentref.Source(strings.TrimSpace(source)), MountID: strings.TrimSpace(mountID), Path: path}
	if strings.TrimSpace(collaborationID) != "" {
		locator.Source = contentref.SourceCollaboration
		locator.MountID = ""
		locator.Path = path
		return access.Locator{}, errors.New("collaboration mutation is not available in this phase")
	}
	switch locator.Source {
	case contentref.SourcePersonal:
		if locator.MountID != "" {
			return access.Locator{}, errors.New("personal locator cannot include mountId")
		}
	case contentref.SourceCommonMount:
		if locator.MountID == "" {
			return access.Locator{}, errors.New("common mount locator requires mountId")
		}
	default:
		return access.Locator{}, errors.New("source must be personal or common_mount")
	}
	return locator, nil
}

func (s *Server) memberDownload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	locator, err := memberLocatorFromQuery(r)
	if err != nil || strings.TrimSpace(locator.Path) == "" || locator.Path == "." {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "a file locator is required")
		return
	}
	mount, err := s.guard.Authorize(r.Context(), access.CheckRequest{
		Subject: sessionSubject(session.AccountID), Scope: aitoken.ScopeFilesMetadata, Locator: locator,
		RequiredPermission: domain.ContentPermissionViewer,
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "file access is not available to this session")
		return
	}
	file, info, err := files.NewService().OpenFile(files.Mount{Root: mount.Root, Mode: mount.Mode, Kind: string(mount.StorageKind)}, mount.RelativePath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "file was not found")
		return
	}
	defer file.Close()
	metadata, err := transfer.DownloadMetadataFromFileInfo(info)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "file was not found")
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", metadata.ETag)
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" || strings.EqualFold(r.URL.Query().Get("disposition"), "inline") {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename="+strconv.Quote(filepath.Base(locator.Path)))
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		byteRange, err := transfer.ParseByteRange(rangeHeader, metadata.Size)
		if err != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", metadata.Size))
			httpx.WriteError(w, r, http.StatusRequestedRangeNotSatisfiable, "invalid_range", "requested byte range is not satisfiable")
			return
		}
		if _, err := file.Seek(byteRange.Start, io.SeekStart); err != nil {
			writeDBError(w, r, err)
			return
		}
		w.Header().Set("Content-Range", byteRange.ContentRange(metadata.Size))
		w.Header().Set("Content-Length", strconv.FormatInt(byteRange.Length(), 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.CopyN(w, file, byteRange.Length())
		return
	}
	http.ServeContent(w, r, filepath.Base(locator.Path), metadata.ModTime, file)
}

func (s *Server) memberSearch(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	locator, err := memberLocatorFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	result, err := s.memberFiles.Search(r.Context(), sessionSubject(session.AccountID), memberfiles.SearchRequest{
		Source: locator.Source, MountID: locator.MountID, Query: r.URL.Query().Get("q"),
		Limit: parseIntDefault(r.URL.Query().Get("limit"), 50), Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "search is not available to this session")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) memberCreateDirectory(w http.ResponseWriter, r *http.Request) {
	var req memberMutationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	locator, err := req.locator()
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "name is required")
		return
	}
	result, err := s.memberFiles.CreateDirectory(r.Context(), sessionSubject(session.AccountID), locator, req.Name)
	if err != nil {
		writeMemberFilesError(w, r, err, "creating a directory requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"relativePath": result.RelativePath})
}

type memberMutationRequest struct {
	Source          string `json:"source"`
	MountID         string `json:"mountId"`
	CollaborationID string `json:"collaborationId"`
	Path            string `json:"path"`
	From            string `json:"from"`
	ToPath          string `json:"toPath"`
	ToDir           string `json:"toDir"`
	Name            string `json:"name"`
}

func (req memberMutationRequest) locator() (access.Locator, error) {
	pathValue := req.Path
	if pathValue == "" {
		pathValue = req.From
	}
	return memberLocatorFromJSON(req.Source, req.MountID, req.CollaborationID, pathValue)
}

func (s *Server) memberRename(w http.ResponseWriter, r *http.Request) {
	var req memberMutationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	source, err := req.locator()
	if err != nil || strings.TrimSpace(req.ToPath) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "source and toPath are required")
		return
	}
	result, err := s.memberFiles.RenameSecure(r.Context(), sessionSubject(session.AccountID), source, req.ToPath, nil)
	if err != nil {
		writeMemberFilesError(w, r, err, "renaming requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
}

func (s *Server) memberMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source          string `json:"source"`
		MountID         string `json:"mountId"`
		CollaborationID string `json:"collaborationId"`
		From            string `json:"from"`
		ToSource        string `json:"toSource"`
		ToMountID       string `json:"toMountId"`
		ToPath          string `json:"toPath"`
		ToDir           string `json:"toDir"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	source, err := memberLocatorFromJSON(req.Source, req.MountID, req.CollaborationID, req.From)
	if err != nil || strings.TrimSpace(req.ToPath) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", "source and toPath are required")
		return
	}
	destination, err := memberLocatorFromJSON(req.ToSource, req.ToMountID, "", req.ToPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	result, err := s.memberFiles.MoveSecure(r.Context(), sessionSubject(session.AccountID), source, destination, nil)
	if err != nil {
		writeMemberFilesError(w, r, err, "moving requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
}

func (s *Server) memberCopy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source          string `json:"source"`
		MountID         string `json:"mountId"`
		CollaborationID string `json:"collaborationId"`
		From            string `json:"from"`
		ToSource        string `json:"toSource"`
		ToMountID       string `json:"toMountId"`
		ToPath          string `json:"toPath"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	source, err := memberLocatorFromJSON(req.Source, req.MountID, req.CollaborationID, req.From)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	destination, err := memberLocatorFromJSON(req.ToSource, req.ToMountID, "", req.ToPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	result, err := s.memberFiles.Copy(r.Context(), sessionSubject(session.AccountID), source, destination)
	if err != nil {
		writeMemberFilesError(w, r, err, "copying requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
}

func (s *Server) memberDelete(w http.ResponseWriter, r *http.Request) {
	var req memberMutationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	locator, err := req.locator()
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	if req.Name == "permanent" || r.URL.Query().Get("permanent") == "1" {
		if _, err := s.memberFiles.DeletePermanentlySecure(r.Context(), sessionSubject(session.AccountID), locator, nil); err != nil {
			writeMemberFilesError(w, r, err, "deleting requires editor permission")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	item, err := s.memberFiles.TrashSecure(r.Context(), sessionSubject(session.AccountID), locator, nil)
	if err != nil {
		writeMemberFilesError(w, r, err, "deleting requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": item.TrashID, "originalPath": item.OriginalPath, "name": item.Name, "kind": item.Kind, "size": item.Size, "deletedAt": item.DeletedAt, "trashRelativePath": item.TrashRelativePath})
}

func (s *Server) memberTrashList(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	locator, err := memberLocatorFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	result, err := s.memberFiles.ListTrashViewer(r.Context(), sessionSubject(session.AccountID), locator)
	if err != nil {
		writeMemberFilesError(w, r, err, "trash is not available to this session")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": result.Items})
}

func (s *Server) memberTrashRestore(w http.ResponseWriter, r *http.Request) {
	var req memberMutationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	locator, err := req.locator()
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	result, err := s.memberFiles.RestoreTrashSecure(r.Context(), sessionSubject(session.AccountID), locator, r.PathValue("trashId"), nil)
	if err != nil {
		writeMemberFilesError(w, r, err, "restoring trash requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"relativePath": result.RelativePath})
}

func (s *Server) memberTrashPurge(w http.ResponseWriter, r *http.Request) {
	locator, err := memberLocatorFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if _, err := s.memberFiles.PurgeTrash(r.Context(), sessionSubject(session.AccountID), locator, r.PathValue("trashId")); err != nil {
		writeMemberFilesError(w, r, err, "purging trash requires editor permission")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) memberTrashEmpty(w http.ResponseWriter, r *http.Request) {
	locator, err := memberLocatorFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	result, err := s.memberFiles.EmptyTrash(r.Context(), sessionSubject(session.AccountID), locator)
	if err != nil {
		writeMemberFilesError(w, r, err, "emptying trash requires editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"removed": result.AffectedCount})
}

func sessionSubject(accountID string) access.Subject { return access.Subject{AccountID: accountID} }
