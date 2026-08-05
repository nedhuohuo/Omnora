package server

import (
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/share"
	"omnora/internal/storage"
	"omnora/internal/transfer"
)

// requireShareSession resolves the share portal session cookie into the
// share it authorizes. It rejects requests that carry a share secret in the
// URL (query string or Referer), mirroring createShareSession.
func (s *Server) requireShareSession(r *http.Request) (share.SessionPrincipal, error) {
	db := s.sqlDB()
	if db == nil {
		return share.SessionPrincipal{}, &share.ExchangeError{Code: share.CodeShareUnavailable}
	}
	if requestCarriesShareSecretInURL(r) {
		return share.SessionPrincipal{}, &share.ExchangeError{Code: share.CodeShareUnavailable}
	}
	cookie, err := r.Cookie(shareSessionCookieName)
	if err != nil || cookie.Value == "" {
		return share.SessionPrincipal{}, &share.ExchangeError{Code: share.CodeShareUnavailable}
	}
	return share.NewService(db).VerifySession(r.Context(), cookie.Value)
}

func writeShareSessionError(w http.ResponseWriter, r *http.Request, err error) {
	var exchangeErr *share.ExchangeError
	if errors.As(err, &exchangeErr) {
		httpx.WriteError(w, r, http.StatusUnauthorized, string(exchangeErr.Code), "share session is not valid")
		return
	}
	httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "share session is not valid")
}

func (s *Server) shareCurrent(w http.ResponseWriter, r *http.Request) {
	principal, err := s.requireShareSession(r)
	if err != nil {
		writeShareSessionError(w, r, err)
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), principal.SpaceID, principal.MountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	entry, err := lookupMountEntry(files.Mount{Root: mount.Root, Mode: mount.Mode}, principal.RelativePath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "share target was not found")
		return
	}
	payload := map[string]any{
		"path":          path.Base(principal.RelativePath),
		"allowPreview":  principal.AllowPreview,
		"allowDownload": principal.AllowDownload,
		"expiresAt":     principal.ShareExpires,
		"kind":          entry.Kind,
		"previewKind":   entry.PreviewKind,
		"size":          entry.Size,
		"modifiedAt":    entry.ModifiedAt,
	}
	if principal.RelativePath == "" || principal.RelativePath == "." {
		payload["path"] = "."
		payload["kind"] = files.EntryKindDir
		payload["previewKind"] = files.PreviewKindUnknownDownload
	}
	httpx.WriteJSON(w, http.StatusOK, payload)
}

func (s *Server) shareChildren(w http.ResponseWriter, r *http.Request) {
	principal, err := s.requireShareSession(r)
	if err != nil {
		writeShareSessionError(w, r, err)
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), principal.SpaceID, principal.MountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	fullPath, ok := s.resolveShareBoundPath(w, r, principal, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	listing, err := files.NewService().ListDirectory(files.Mount{Root: mount.Root, Mode: mount.Mode}, fullPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"relativePath": shareRelativeDisplayPath(principal.RelativePath, listing.RelativePath),
		"readOnly":     true,
		"entries":      listing.Entries,
	})
}

func (s *Server) shareDownload(w http.ResponseWriter, r *http.Request) {
	principal, err := s.requireShareSession(r)
	if err != nil {
		writeShareSessionError(w, r, err)
		return
	}
	if !principal.AllowDownload {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "downloads are not allowed for this share")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), principal.SpaceID, principal.MountID)
	if writeMountLoadError(w, r, err) {
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	fullPath, ok := s.resolveShareBoundPath(w, r, principal, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	file, info, err := files.NewService().OpenFile(files.Mount{Root: mount.Root, Mode: mount.Mode}, fullPath)
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
	if err := share.NewService(s.sqlDB()).IncrementDownload(r.Context(), principal.ShareID, principal.Generation); err != nil {
		writeShareSessionError(w, r, err)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", metadata.ETag)
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename="+strconv.Quote(path.Base(fullPath)))
	_ = s.recordAudit(r, "share_download", "share", principal.ShareID, "{}")
	http.ServeContent(w, r, path.Base(fullPath), metadata.ModTime, file)
}

// resolveShareBoundPath joins a share-relative request path with the
// share's mount-relative root, keeping the result confined to the share
// boundary. storage.CleanRelativePath rejects any ".." that would otherwise
// escape "." in the share's own frame, so the join below can never resolve
// outside of principal.RelativePath.
func (s *Server) resolveShareBoundPath(w http.ResponseWriter, r *http.Request, principal share.SessionPrincipal, requestedPath string) (string, bool) {
	cleaned, err := storage.CleanRelativePath(requestedPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "path is invalid")
		return "", false
	}
	root := principal.RelativePath
	if root == "" {
		root = "."
	}
	if cleaned == "." {
		return root, true
	}
	if root == "." {
		return cleaned, true
	}
	return root + "/" + cleaned, true
}

func shareRelativeDisplayPath(shareRoot, mountRelativePath string) string {
	if shareRoot == "" || shareRoot == "." {
		return mountRelativePath
	}
	trimmed := strings.TrimPrefix(mountRelativePath, shareRoot)
	trimmed = strings.TrimPrefix(trimmed, "/")
	if trimmed == "" {
		return "."
	}
	return trimmed
}
