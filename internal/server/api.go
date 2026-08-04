package server

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/audit"
	"omnora/internal/catalog"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/jobs"
	"omnora/internal/mountid"
	"omnora/internal/share"
	"omnora/internal/storage"
	"omnora/internal/totp"
	"omnora/internal/transfer"
)

const sessionCookieName = "omnora_session"

type routeGroupDTO struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Exposed bool   `json:"exposed"`
	Entry   string `json:"entry"`
	Risk    string `json:"risk"`
	Tone    string `json:"tone"`
}

type mountDTO struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Space  string `json:"space"`
	Mode   string `json:"mode"`
	Index  string `json:"index"`
	Health string `json:"health"`
	Tone   string `json:"tone"`
}

type fileDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Size       string `json:"size"`
	Modified   string `json:"modified"`
	Mount      string `json:"mount"`
	Access     string `json:"access"`
	Status     string `json:"status"`
	StatusTone string `json:"statusTone"`
	Index      string `json:"index"`
}

type transferDTO struct {
	Name      string `json:"name"`
	Operation string `json:"operation"`
	Progress  int    `json:"progress"`
	State     string `json:"state"`
	Tone      string `json:"tone"`
}

type adminRiskDTO struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Tone  string `json:"tone"`
}

type auditDTO struct {
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Event  string `json:"event"`
	Target string `json:"target"`
	Result string `json:"result"`
}

type shareDTO struct {
	ID         string `json:"id"`
	Target     string `json:"target"`
	Capability string `json:"capability"`
	Expires    string `json:"expires"`
	Use        string `json:"use"`
	Status     string `json:"status"`
	Tone       string `json:"tone"`
}

type tokenDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Boundary string `json:"boundary"`
	Status   string `json:"status"`
	Tone     string `json:"tone"`
}

type bootstrapDTO struct {
	RouteGroups []routeGroupDTO `json:"routeGroups"`
	Mounts      []mountDTO      `json:"mounts"`
	Files       []fileDTO       `json:"files"`
	Transfers   []transferDTO   `json:"transfers"`
	AdminRisks  []adminRiskDTO  `json:"adminRisks"`
	AuditRows   []auditDTO      `json:"auditRows"`
	Shares      []shareDTO      `json:"shares"`
	Tokens      []tokenDTO      `json:"tokens"`
}

func (s *Server) apiRoutes() {
	s.mux.Handle("GET /api/v1/bootstrap", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.bootstrap)))
	s.mux.Handle("POST /api/v1/initialize", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.initialize)))
	s.mux.Handle("GET /api/v1/auth/session", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.currentSession)))
	s.mux.Handle("POST /api/v1/auth/session", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createSession)))
	s.mux.Handle("DELETE /api/v1/auth/session", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeSession)))
	s.mux.Handle("POST /api/v1/account/totp/setup", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.setupTOTP)))
	s.mux.Handle("POST /api/v1/account/totp/confirm", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.confirmTOTP)))
	s.mux.Handle("GET /api/v1/spaces", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listSpaces)))
	s.mux.Handle("GET /api/v1/spaces/{spaceId}/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listMounts)))
	s.mux.Handle("GET /api/v1/spaces/{spaceId}/mounts/{mountId}/children", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listChildren)))
	s.mux.Handle("POST /api/v1/spaces/{spaceId}/mounts/{mountId}/directories", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createDirectory)))
	s.mux.Handle("GET /api/v1/spaces/{spaceId}/mounts/{mountId}/download", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.downloadFile)))
	s.mux.Handle("GET /api/v1/spaces/{spaceId}/search", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.searchSpace)))
	s.mux.Handle("POST /api/v1/shares", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createShare)))
	s.mux.Handle("GET /api/v1/ai-tokens", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAITokens)))
	s.mux.Handle("POST /api/v1/ai-tokens", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createAIToken)))
	s.mux.Handle("DELETE /api/v1/ai-tokens/{tokenId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeAIToken)))
	s.mux.Handle("POST /api/v1/uploads", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createUpload)))
	s.mux.Handle("GET /api/v1/uploads/{uploadId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.getUpload)))
	s.mux.Handle("PUT /api/v1/uploads/{uploadId}/parts/{partNumber}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.uploadPart)))
	s.mux.Handle("POST /api/v1/uploads/{uploadId}/complete", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.completeUpload)))
	s.mux.Handle("DELETE /api/v1/uploads/{uploadId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.cancelUpload)))
	s.mux.Handle("GET /api/v1/admin/spaces", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminSpaces)))
	s.mux.Handle("GET /api/v1/admin/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminMounts)))
	s.mux.Handle("POST /api/v1/admin/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createMount)))
	s.mux.Handle("GET /api/v1/admin/index-jobs", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listIndexJobs)))
	s.mux.Handle("POST /api/v1/admin/index-jobs", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.enqueueIndexJob)))
	s.mux.Handle("POST /api/v1/admin/index-jobs/{jobId}/run", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.runIndexJob)))
	s.mux.Handle("GET /api/v1/audit/events", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAuditEvents)))
	s.mux.Handle("POST /api/v1/share-sessions", s.gate(domain.RouteGroupShare, http.HandlerFunc(s.createShareSession)))
	s.mux.Handle("POST /mcp", s.gate(domain.RouteGroupMCP, http.HandlerFunc(s.handleMCP)))
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	payload := bootstrapDTO{
		RouteGroups: s.routeGroups(),
		Transfers: []transferDTO{
			{Name: "No active transfers", Operation: "transfer center", Progress: 0, State: "idle", Tone: "muted"},
		},
		Tokens: []tokenDTO{},
	}

	db := s.sqlDB()
	if db == nil {
		payload.AdminRisks = []adminRiskDTO{{Label: "Database", Value: "not configured", Tone: "danger"}}
		httpx.WriteJSON(w, http.StatusOK, payload)
		return
	}

	session, authenticated := s.optionalSession(r)
	payload.Mounts = s.bootstrapMounts(r, db, session, authenticated)
	payload.Files = s.bootstrapFiles(r, db, session, authenticated)
	payload.Shares = s.bootstrapShares(r, db, session, authenticated)
	payload.Tokens = s.bootstrapTokens(r, db, session, authenticated)
	if authenticated && s.isAdmin(r, session.AccountID) {
		payload.AuditRows = s.bootstrapAudit(r, db)
		payload.AdminRisks = s.bootstrapRisks(db)
	} else if authenticated {
		payload.AdminRisks = []adminRiskDTO{{Label: "Session", Value: "authenticated", Tone: "ok"}}
	} else {
		payload.AdminRisks = []adminRiskDTO{{Label: "Session", Value: "not authenticated", Tone: "muted"}}
	}
	httpx.WriteJSON(w, http.StatusOK, payload)
}

func (s *Server) initialize(w http.ResponseWriter, r *http.Request) {
	db := s.requireDB(w, r)
	if db == nil {
		return
	}
	var req struct {
		Token          string `json:"token"`
		Email          string `json:"email"`
		DisplayName    string `json:"displayName"`
		DisplayNameAlt string `json:"display_name"`
		Password       string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.DisplayNameAlt
	}
	created, err := identity.New(db, identity.Options{}).Initialize(r.Context(), identity.InitializationRequest{
		Token:       req.Token,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
	})
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "system_initialize", "account", created.Account.ID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"user":  accountResponse(created.Account),
		"space": spaceResponse(created.PersonalSpace, domain.SpacePermissionManager),
	})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	db := s.requireDB(w, r)
	if db == nil {
		return
	}
	var req struct {
		Login       string `json:"login"`
		Password    string `json:"password"`
		TOTPCode    string `json:"totpCode"`
		TOTPCodeAlt string `json:"totp_code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TOTPCode == "" {
		req.TOTPCode = req.TOTPCodeAlt
	}
	svc := identity.New(db, identity.Options{})
	account, err := svc.Authenticate(r.Context(), req.Login, req.Password)
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	if ok, err := s.verifyLoginTOTP(r, account.ID, req.TOTPCode); err != nil {
		writeDBError(w, r, err)
		return
	} else if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, "totp_required", "a valid TOTP code is required")
		return
	}
	issued, err := svc.CreateSession(r.Context(), identity.SessionRequest{
		AccountID: account.ID,
		TTL:       8 * time.Hour,
	})
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	http.SetCookie(w, sessionCookie(r, issued.Token, issued.Session.ExpiresAt))
	_ = s.recordAudit(r, "login", "account", account.ID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"userId":    account.ID,
		"expiresAt": issued.Session.ExpiresAt,
		"isAdmin":   s.isAdmin(r, account.ID),
	})
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"userId":    session.AccountID,
		"expiresAt": session.ExpiresAt,
		"isAdmin":   s.isAdmin(r, session.AccountID),
	})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	db := s.requireDB(w, r)
	if db == nil {
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := identity.New(db, identity.Options{}).RevokeSession(r.Context(), cookie.Value); err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setupTOTP(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	account, err := s.loadAccountForTOTP(r, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	uri, err := totp.OTPAuthURI(totp.URIOptions{
		Issuer:      "Omnora",
		AccountName: account.Email,
		Secret:      secret,
	})
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	sealedSecret, err := s.encryptTOTPSecret(secret)
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "totp_key_unavailable", err.Error())
		return
	}
	_, err = s.sqlDB().ExecContext(r.Context(), `
	UPDATE accounts
	SET totp_secret_ciphertext = ?, totp_confirmed_at = NULL, updated_at = ?
	WHERE id = ? AND status = 'active'
	`, sealedSecret, time.Now().UTC().Format(time.RFC3339Nano), session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "totp_setup", "account", session.AccountID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"secret":     secret,
		"otpauthUri": uri,
	})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	account, err := s.loadAccountForTOTP(r, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if account.TOTPSecret == "" || !totp.Verify(account.TOTPSecret, req.Code, time.Now().UTC()) {
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_totp", "TOTP code is not valid")
		return
	}
	_, err = s.sqlDB().ExecContext(r.Context(), `
UPDATE accounts
SET totp_required = 1, totp_confirmed_at = ?, updated_at = ?
WHERE id = ? AND status = 'active'
`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "totp_confirm", "account", session.AccountID, "{}")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "enabled"})
}

func (s *Server) listSpaces(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT sp.id, sp.kind, sp.name, sm.permission
FROM spaces sp
JOIN space_members sm ON sm.space_id = sp.id
WHERE sm.account_id = ? AND sp.status = 'active'
ORDER BY sp.kind, sp.name
`, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()

	items := make([]map[string]string, 0)
	for rows.Next() {
		var id, kind, name, permission string
		if err := rows.Scan(&id, &kind, &name, &permission); err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, map[string]string{
			"id": id, "type": kind, "name": name, "role": permission,
		})
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listAdminSpaces(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can list spaces")
		return
	}

	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT id, kind, name
FROM spaces
WHERE status = 'active'
ORDER BY kind, name, id
`)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()

	items := make([]map[string]string, 0)
	for rows.Next() {
		var id, kind, name string
		if err := rows.Scan(&id, &kind, &name); err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, map[string]string{"id": id, "type": kind, "name": name})
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listAdminMounts(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can list mounts")
		return
	}

	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT m.id, m.display_name, sp.name, m.mode, m.index_enabled, m.status
FROM mounts m
JOIN spaces sp ON sp.id = m.space_id
WHERE m.status <> 'deleted'
ORDER BY sp.kind, sp.name, m.display_name, m.id
`)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items, err := scanMountDTOs(rows)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listMounts(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	if !s.canReadSpace(r, session.AccountID, spaceID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "space is not available to this session")
		return
	}
	mounts, err := queryMounts(r, s.sqlDB(), session.AccountID, spaceID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": mounts})
}

func (s *Server) listChildren(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.canReadSpace(r, session.AccountID, spaceID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "space is not available to this session")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	listing, err := files.NewService().ListDirectory(files.Mount{Root: mount.Root, Mode: mount.Mode}, r.URL.Query().Get("path"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"relativePath": listing.RelativePath,
		"readOnly":     listing.ReadOnly,
		"entries":      listing.Entries,
	})
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.canReadSpace(r, session.AccountID, spaceID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "space is not available to this session")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	relativePath, err := storage.CleanRelativePath(r.URL.Query().Get("path"))
	if err != nil || relativePath == "." {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", "download path is invalid")
		return
	}
	file, info, err := files.NewService().OpenFile(files.Mount{Root: mount.Root, Mode: mount.Mode}, relativePath)
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
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(relativePath)))
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
	http.ServeContent(w, r, filepath.Base(relativePath), metadata.ModTime, file)
}

func (s *Server) createDirectory(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	mountID := r.PathValue("mountId")
	if !s.hasSpacePermission(r, session.AccountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "creating a directory requires editor permission")
		return
	}
	var req struct {
		ParentPath string `json:"parentPath"`
		Name       string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), spaceID, mountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	created, err := files.NewService().CreateDirectory(files.Mount{Root: mount.Root, Mode: mount.Mode}, req.ParentPath, req.Name)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	_ = s.recordAudit(r, "directory_create", "directory", created, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"relativePath": created})
}

func (s *Server) searchSpace(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	spaceID := r.PathValue("spaceId")
	if !s.canReadSpace(r, session.AccountID, spaceID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "space is not available to this session")
		return
	}
	result, err := catalog.NewService(s.sqlDB()).Search(r.Context(), catalog.SearchOptions{
		SpaceID: spaceID,
		Query:   r.URL.Query().Get("q"),
		Cursor:  r.URL.Query().Get("cursor"),
		Limit:   parseIntDefault(r.URL.Query().Get("limit"), 50),
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_search", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		SpaceID    string `json:"spaceId"`
		MountID    string `json:"mountId"`
		ParentPath string `json:"parentPath"`
		FileName   string `json:"fileName"`
		Size       int64  `json:"size"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.hasSpacePermission(r, session.AccountID, req.SpaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "upload requires editor permission")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), req.SpaceID, req.MountID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
		return
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	targetPath := pathJoinForUpload(req.ParentPath, req.FileName)
	if err := files.NewService().ValidateWritableTarget(files.Mount{Root: mount.Root, Mode: mount.Mode}, targetPath); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	service, err := transfer.NewService(transfer.Options{
		MountRoot: mount.Root,
		TempRoot:  filepath.Join(mount.Root, ".omnora", "tmp", "uploads"),
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	defer service.Close()
	upload, err := service.CreateUploadSession(transfer.CreateUploadSessionRequest{
		TargetPath:   targetPath,
		ExpectedSize: req.Size,
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	_, err = s.sqlDB().ExecContext(r.Context(), `
INSERT INTO upload_sessions(id, account_id, space_id, mount_id, target_relative_path, declared_size, part_size, temp_dir, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, upload.ID, session.AccountID, req.SpaceID, req.MountID, upload.TargetPath, upload.ExpectedSize, 32*1024, filepath.Join(mount.Root, ".omnora", "tmp", "uploads"), expiresAt.Format(time.RFC3339Nano))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "upload_create", "upload", upload.ID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": upload.ID, "expiresAt": expiresAt, "partSize": 32 * 1024})
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	upload, mount, err := s.loadUpload(r, session.AccountID, r.PathValue("uploadId"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "upload session was not found")
		return
	}
	if !s.canContinueUpload(w, r, session.AccountID, upload.SpaceID, mount) {
		return
	}
	service, err := transfer.NewService(transfer.Options{MountRoot: mount.Root, TempRoot: upload.TempDir})
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	defer service.Close()
	resumed, err := service.ResumeUploadSession(upload.ID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":           resumed.ID,
		"targetPath":   resumed.TargetPath,
		"expectedSize": resumed.ExpectedSize,
		"receivedSize": resumed.ReceivedSize,
		"parts":        resumed.Parts,
		"partSize":     upload.PartSize,
		"expiresAt":    upload.ExpiresAt,
	})
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	upload, mount, err := s.loadUpload(r, session.AccountID, r.PathValue("uploadId"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "upload session was not found")
		return
	}
	if !s.canContinueUpload(w, r, session.AccountID, upload.SpaceID, mount) {
		return
	}
	partNumber, err := strconv.Atoi(r.PathValue("partNumber"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "part number is invalid")
		return
	}
	service, err := transfer.NewService(transfer.Options{MountRoot: mount.Root, TempRoot: upload.TempDir})
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	defer service.Close()
	part, err := service.WritePart(upload.ID, partNumber, r.Body)
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	_, _ = s.sqlDB().ExecContext(r.Context(), `
INSERT INTO upload_parts(upload_id, part_number, size_bytes)
VALUES (?, ?, ?)
ON CONFLICT(upload_id, part_number) DO UPDATE SET size_bytes = excluded.size_bytes, created_at = CURRENT_TIMESTAMP
`, upload.ID, part.Number, part.Size)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	upload, mount, err := s.loadUpload(r, session.AccountID, r.PathValue("uploadId"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "upload session was not found")
		return
	}
	if !s.canContinueUpload(w, r, session.AccountID, upload.SpaceID, mount) {
		return
	}
	service, err := transfer.NewService(transfer.Options{MountRoot: mount.Root, TempRoot: upload.TempDir})
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	defer service.Close()
	completed, err := service.CompleteUpload(upload.ID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "upload_conflict", err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.sqlDB().ExecContext(r.Context(), "UPDATE upload_sessions SET status = 'completed', completed_at = ? WHERE id = ?", now, upload.ID)
	_ = s.recordAudit(r, "upload_complete", "upload", upload.ID, "{}")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"targetPath": completed.TargetPath, "size": completed.Size})
}

func (s *Server) cancelUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	upload, mount, err := s.loadUpload(r, session.AccountID, r.PathValue("uploadId"))
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.canContinueUpload(w, r, session.AccountID, upload.SpaceID, mount) {
		return
	}
	if service, err := transfer.NewService(transfer.Options{MountRoot: mount.Root, TempRoot: upload.TempDir}); err == nil {
		_ = service.CancelUpload(upload.ID)
		_ = service.Close()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.sqlDB().ExecContext(r.Context(), "UPDATE upload_sessions SET status = 'canceled', canceled_at = ? WHERE id = ?", now, upload.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listIndexJobs(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can list index jobs")
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT id, kind, priority, status, payload_json, checkpoint_json, attempts, max_attempts,
       COALESCE(claimed_at, ''), COALESCE(claimed_by, ''), COALESCE(last_error, ''),
       created_at, updated_at, COALESCE(completed_at, '')
FROM jobs
ORDER BY created_at DESC, id DESC
LIMIT ?
	`, parseIntDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var job jobs.Job
		var status string
		var claimedAt, claimedBy, lastError, completedAt string
		if err := rows.Scan(
			&job.ID,
			&job.Kind,
			&job.Priority,
			&status,
			&job.PayloadJSON,
			&job.CheckpointJSON,
			&job.Attempts,
			&job.MaxAttempts,
			&claimedAt,
			&claimedBy,
			&lastError,
			&job.CreatedAt,
			&job.UpdatedAt,
			&completedAt,
		); err != nil {
			writeDBError(w, r, err)
			return
		}
		job.Status = jobs.Status(status)
		items = append(items, jobResponse(job, claimedAt, claimedBy, lastError, completedAt))
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) enqueueIndexJob(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can manage index jobs")
		return
	}
	var req struct {
		MountID    string `json:"mountId"`
		MountIDAlt string `json:"mount_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.MountID == "" {
		req.MountID = req.MountIDAlt
	}
	if strings.TrimSpace(req.MountID) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_mount", "mountId is required")
		return
	}
	mount, err := s.loadCatalogMount(r, req.MountID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusNotFound, "mount_not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if mount.Status != catalog.MountStatusActive || !mount.IndexEnabled {
		httpx.WriteError(w, r, http.StatusConflict, "mount_not_indexable", "mount must be active with indexing enabled")
		return
	}
	payload, _ := json.Marshal(map[string]string{"mount_id": req.MountID})
	job, err := jobs.NewStore(s.sqlDB()).Enqueue(r.Context(), jobs.EnqueueOptions{
		Kind:        "catalog_scan",
		Priority:    100,
		PayloadJSON: string(payload),
	})
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "index_job_enqueue", "job", job.ID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, job)
}

func (s *Server) runIndexJob(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can run index jobs")
		return
	}
	store := jobs.NewStore(s.sqlDB())
	job, err := store.Get(r.Context(), r.PathValue("jobId"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "job was not found")
		return
	}
	var payload struct {
		MountID string `json:"mount_id"`
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.MountID == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_job", "job payload is invalid")
		return
	}
	mount, err := s.loadCatalogMount(r, payload.MountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if err := s.verifyLoadedMountIdentity(r, mountForListing{ID: mount.ID, Root: mount.Root, IdentityJSON: mount.IdentityJSON}); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return
	}
	claimed, err := store.ClaimByID(r.Context(), job.ID, "inline-http-worker")
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if claimed == nil || claimed.ID != job.ID {
		httpx.WriteError(w, r, http.StatusConflict, "job_not_claimed", "another job is running or this job is not queued")
		return
	}
	var checkpoint struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(claimed.CheckpointJSON), &checkpoint); err != nil {
		_ = store.Fail(r.Context(), claimed.ID, err)
		httpx.WriteError(w, r, http.StatusConflict, "invalid_job", "job checkpoint is invalid")
		return
	}
	result, err := catalog.NewService(s.sqlDB()).ScanBatch(r.Context(), mount, catalog.ScanOptions{
		BatchSize: catalog.DefaultBatchSize,
		Cursor:    checkpoint.Cursor,
	})
	if err != nil {
		_ = store.Fail(r.Context(), job.ID, err)
		httpx.WriteError(w, r, http.StatusConflict, "index_failed", err.Error())
		return
	}
	if !result.Done {
		checkpoint, _ := json.Marshal(map[string]string{"cursor": result.NextCursor})
		if err := store.Requeue(r.Context(), claimed.ID, string(checkpoint)); err != nil {
			writeDBError(w, r, err)
			return
		}
	} else {
		_ = store.Complete(r.Context(), claimed.ID)
	}
	_ = s.recordAudit(r, "index_job_run", "job", job.ID, "{}")
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can view global audit")
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT occurred_at, COALESCE(actor_account_id, 'system'), COALESCE(route_group, ''), action, target_type, COALESCE(target_id, ''), metadata_json
FROM audit_events
ORDER BY id DESC
LIMIT ?
`, parseIntDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var occurredAt, actor, routeGroup, action, targetType, targetID, metadata string
		if err := rows.Scan(&occurredAt, &actor, &routeGroup, &action, &targetType, &targetID, &metadata); err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, map[string]string{
			"occurredAt": occurredAt, "actor": actor, "routeGroup": routeGroup, "action": action,
			"targetType": targetType, "targetId": targetID, "metadata": metadata,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	principal, err := s.requireAIPrincipal(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "AI token is not valid")
		return
	}
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Method {
	case "tools/list":
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"tools": []string{"spaces.list", "files.search"}})
	case "spaces.list":
		if !principal.HasScope(aitoken.ScopeSpacesRead) {
			httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "scope is not allowed")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"boundaries": principal.Boundaries})
	case "files.search":
		if !principal.HasScope(aitoken.ScopeSearchRead) {
			httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "scope is not allowed")
			return
		}
		if len(principal.Boundaries) == 0 {
			httpx.WriteJSON(w, http.StatusOK, catalog.SearchResult{})
			return
		}
		query, _ := req.Params["q"].(string)
		result, err := s.searchAIPrincipal(r, principal, query, 20)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_search", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, result)
	default:
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "MCP method is not available")
	}
}

func (s *Server) createMount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.isAdmin(r, session.AccountID) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "only system administrators can register mounts")
		return
	}

	var req struct {
		SpaceID         string `json:"spaceId"`
		SpaceIDAlt      string `json:"space_id"`
		DisplayName     string `json:"displayName"`
		DisplayNameAlt  string `json:"display_name"`
		RootPath        string `json:"rootPath"`
		RootPathAlt     string `json:"root_path"`
		Kind            string `json:"kind"`
		Mode            string `json:"mode"`
		IndexEnabled    bool   `json:"indexEnabled"`
		IndexEnabledAlt bool   `json:"index_enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SpaceID == "" {
		req.SpaceID = req.SpaceIDAlt
	}
	if req.DisplayName == "" {
		req.DisplayName = req.DisplayNameAlt
	}
	if req.RootPath == "" {
		req.RootPath = req.RootPathAlt
	}
	if req.IndexEnabledAlt {
		req.IndexEnabled = true
	}

	mount, err := s.validateMountRequest(r, req.SpaceID, req.DisplayName, req.RootPath, req.Kind, req.Mode)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_input"
		if errors.Is(err, errMountConflict) {
			status = http.StatusConflict
			code = "mount_conflict"
		}
		if errors.Is(err, errMountIdentityUnverifiable) {
			status = http.StatusConflict
			code = "mount_identity_unverifiable"
		}
		httpx.WriteError(w, r, status, code, err.Error())
		return
	}

	indexEnabled := 0
	if req.IndexEnabled {
		indexEnabled = 1
	}
	mountID := "mnt_" + httpx.NewRequestID()
	identityJSON, err := json.Marshal(mount.Identity)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_, err = s.sqlDB().ExecContext(r.Context(), `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?)
`, mountID, req.SpaceID, mount.DisplayName, mount.RootPath, mount.Kind, mount.Mode, indexEnabled, string(identityJSON))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			httpx.WriteError(w, r, http.StatusConflict, "mount_conflict", "mount display name already exists in this space")
			return
		}
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "mount_create", "mount", mountID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, mountDTO{
		ID: mountID, Name: mount.DisplayName, Space: mount.SpaceName, Mode: displayMountMode(mount.Mode),
		Index: displayIndex(indexEnabled), Health: "active", Tone: "ok",
	})
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		SpaceID         string `json:"spaceId"`
		SpaceIDAlt      string `json:"space_id"`
		MountID         string `json:"mountId"`
		MountIDAlt      string `json:"mount_id"`
		RelativePath    string `json:"relativePath"`
		RelativePathAlt string `json:"relative_path"`
		Password        string `json:"password"`
		AllowPreview    *bool  `json:"allowPreview"`
		AllowDownload   *bool  `json:"allowDownload"`
		MaxVisits       *int   `json:"maxVisits"`
		MaxVisitsAlt    *int   `json:"max_visits"`
		ExpiresAt       string `json:"expiresAt"`
		ExpiresAtAlt    string `json:"expires_at"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SpaceID == "" {
		req.SpaceID = req.SpaceIDAlt
	}
	if req.MountID == "" {
		req.MountID = req.MountIDAlt
	}
	if req.RelativePath == "" {
		req.RelativePath = req.RelativePathAlt
	}
	if req.MaxVisits == nil {
		req.MaxVisits = req.MaxVisitsAlt
	}
	if req.ExpiresAt == "" {
		req.ExpiresAt = req.ExpiresAtAlt
	}
	if !s.hasSpacePermission(r, session.AccountID, req.SpaceID, domain.SpacePermissionManager) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "creating shares requires manager permission")
		return
	}
	mount, err := loadMountForListing(r, s.sqlDB(), req.SpaceID, req.MountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
			return
		}
		writeDBError(w, r, err)
		return
	}
	relativePath, err := s.validateShareTarget(r, mount, req.RelativePath)
	if err != nil {
		if errors.Is(err, errMountIdentityUnverifiable) {
			httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
			return
		}
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}

	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	if req.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "expiresAt must be RFC3339")
			return
		}
		expiresAt = parsed.UTC()
	}
	if !time.Now().UTC().Before(expiresAt) {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "expiresAt must be in the future")
		return
	}
	allowPreview := boolDefault(req.AllowPreview, true)
	allowDownload := boolDefault(req.AllowDownload, true)
	secret, err := share.NewSecret()
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	publicID := "pub_" + httpx.NewRequestID()
	shareID := "shr_" + httpx.NewRequestID()
	var passwordHash any
	if req.Password != "" {
		hash, err := share.HashPassword(req.Password)
		if err != nil {
			writeDBError(w, r, err)
			return
		}
		passwordHash = hash
	}
	var maxVisits any
	if req.MaxVisits != nil {
		if *req.MaxVisits <= 0 {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "maxVisits must be positive")
			return
		}
		maxVisits = *req.MaxVisits
	}
	_, err = s.sqlDB().ExecContext(r.Context(), `
INSERT INTO shares(id, public_id, secret_hash, password_hash, creator_account_id, space_id, mount_id, relative_path, allow_preview, allow_download, max_visits, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, shareID, publicID, share.HashSecret(secret), passwordHash, session.AccountID, req.SpaceID, req.MountID, relativePath, boolInt(allowPreview), boolInt(allowDownload), maxVisits, expiresAt.Format(time.RFC3339Nano))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "share_create", "share", shareID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":        shareID,
		"publicId":  publicID,
		"secret":    secret,
		"fragment":  publicID + "." + secret,
		"expiresAt": expiresAt,
	})
}

func (s *Server) listAITokens(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT t.id, t.public_id, t.name, t.scopes, t.created_at, t.expires_at,
       COALESCE(t.last_used_at, ''), COALESCE(t.revoked_at, ''),
       COALESCE(group_concat(b.space_id || char(31) || b.mount_id || char(31) || b.relative_path, char(30)), '')
FROM ai_tokens t
LEFT JOIN ai_token_boundaries b ON b.token_id = t.id
WHERE t.account_id = ?
GROUP BY t.id
ORDER BY t.created_at DESC, t.id DESC
LIMIT ?
	`, session.AccountID, parseIntDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, publicID, name, scopesJSON, createdAt, expiresAt, lastUsedAt, revokedAt, boundaryList string
		if err := rows.Scan(&id, &publicID, &name, &scopesJSON, &createdAt, &expiresAt, &lastUsedAt, &revokedAt, &boundaryList); err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, aiTokenResponse(id, publicID, name, scopesJSON, createdAt, expiresAt, lastUsedAt, revokedAt, parseBoundaryList(boundaryList)))
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAIToken(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		Name       string   `json:"name"`
		Scopes     []string `json:"scopes"`
		Boundaries []struct {
			SpaceID      string `json:"spaceId"`
			SpaceIDAlt   string `json:"space_id"`
			MountID      string `json:"mountId"`
			MountIDAlt   string `json:"mount_id"`
			Path         string `json:"path"`
			RelativePath string `json:"relativePath"`
		} `json:"boundaries"`
		ExpiresAt    string `json:"expiresAt"`
		ExpiresAtAlt string `json:"expires_at"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ExpiresAt == "" {
		req.ExpiresAt = req.ExpiresAtAlt
	}
	expiresAt, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil || !time.Now().UTC().Before(expiresAt.UTC()) {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "expiresAt must be a future RFC3339 timestamp")
		return
	}
	scopes := make([]aitoken.Scope, 0, len(req.Scopes))
	for _, scope := range req.Scopes {
		scopes = append(scopes, aitoken.Scope(scope))
	}
	boundaries := make([]aitoken.DirectoryBoundary, 0, len(req.Boundaries))
	for _, raw := range req.Boundaries {
		spaceID := raw.SpaceID
		if spaceID == "" {
			spaceID = raw.SpaceIDAlt
		}
		mountID := raw.MountID
		if mountID == "" {
			mountID = raw.MountIDAlt
		}
		relativePath := raw.Path
		if relativePath == "" {
			relativePath = raw.RelativePath
		}
		if !s.canReadSpace(r, session.AccountID, spaceID) || !s.mountBelongsToSpace(r, spaceID, mountID) {
			httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "token boundary is outside this session")
			return
		}
		boundaries = append(boundaries, aitoken.DirectoryBoundary{
			SpaceID: spaceID, MountID: mountID, RelativePath: relativePath,
		})
	}
	if scopeIncluded(scopes, aitoken.ScopeUploadsCreate) {
		for _, boundary := range boundaries {
			if !s.hasSpacePermission(r, session.AccountID, boundary.SpaceID, domain.SpacePermissionEditor) || !s.mountAllowsUpload(r, boundary.SpaceID, boundary.MountID) {
				httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "upload scope requires editor permission on a read-write mount")
				return
			}
		}
	}
	issued, err := aitoken.NewService(s.sqlDB()).Create(r.Context(), aitoken.CreateRequest{
		AccountID:  session.AccountID,
		Name:       req.Name,
		Scopes:     scopes,
		Boundaries: boundaries,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		writeTokenError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "ai_token_create", "ai_token", issued.Token.ID, "{}")
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"token": map[string]any{
			"id":        issued.Token.ID,
			"publicId":  issued.Token.PublicID,
			"name":      issued.Token.Name,
			"scopes":    issued.Token.Scopes,
			"expiresAt": issued.Token.ExpiresAt,
		},
		"secret":      issued.Secret,
		"bearerToken": issued.BearerToken,
	})
}

func (s *Server) revokeAIToken(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE ai_tokens
SET revoked_at = ?, updated_at = ?
WHERE id = ? AND account_id = ? AND revoked_at IS NULL
	`, now, now, r.PathValue("tokenId"), session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if changed != 1 {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "AI token was not found")
		return
	}
	_ = s.recordAudit(r, "ai_token_revoke", "ai_token", r.PathValue("tokenId"), "{}")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createShareSession(w http.ResponseWriter, r *http.Request) {
	db := s.requireDB(w, r)
	if db == nil {
		return
	}
	if requestCarriesShareSecretInURL(r) {
		httpx.WriteError(w, r, http.StatusUnauthorized, "share_unavailable", "share session could not be created")
		return
	}
	var req struct {
		PublicID string `json:"public_id"`
		Secret   string `json:"secret"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := share.NewService(db).Exchange(r.Context(), share.ExchangeRequest{
		PublicID:       req.PublicID,
		FragmentSecret: req.Secret,
		Password:       req.Password,
	})
	if err != nil {
		var exchangeErr *share.ExchangeError
		if errors.As(err, &exchangeErr) {
			status := http.StatusUnauthorized
			if exchangeErr.Code == share.CodeRateLimited {
				status = http.StatusTooManyRequests
			}
			httpx.WriteError(w, r, status, string(exchangeErr.Code), "share session could not be created")
			return
		}
		writeDBError(w, r, err)
		return
	}
	if result.PasswordRequired {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "password_required"})
		return
	}
	http.SetCookie(w, shareSessionCookie(r, result.SessionToken, result.ExpiresAt))
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"status":         "created",
		"shareSessionId": result.SessionID,
		"expiresAt":      result.ExpiresAt,
	})
}

func (s *Server) routeGroups() []routeGroupDTO {
	labels := map[domain.RouteGroup]string{
		domain.RouteGroupMemberWeb: "Member Web",
		domain.RouteGroupAdminWeb:  "Admin Web",
		domain.RouteGroupShare:     "Share",
		domain.RouteGroupREST:      "REST",
		domain.RouteGroupMCP:       "MCP",
		domain.RouteGroupOpenAPI:   "OpenAPI",
	}
	items := make([]routeGroupDTO, 0, len(domain.AllRouteGroups))
	for _, group := range domain.AllRouteGroups {
		exposed := s.cfg.Routes.Enabled(group)
		tone := "muted"
		risk := "closed"
		entry := "disabled"
		if exposed {
			tone = "ok"
			risk = "enabled by explicit route group"
			entry = "lan_http"
		}
		items = append(items, routeGroupDTO{
			ID: string(group), Label: labels[group], Exposed: exposed, Entry: entry, Risk: risk, Tone: tone,
		})
	}
	return items
}

func (s *Server) sqlDB() *sql.DB {
	if s.db == nil {
		return nil
	}
	return s.db.SQL()
}

func (s *Server) requireDB(w http.ResponseWriter, r *http.Request) *sql.DB {
	db := s.sqlDB()
	if db == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "database_unavailable", "database is required")
	}
	return db
}

func (s *Server) optionalSession(r *http.Request) (identity.Session, bool) {
	session, err := s.requireSession(r)
	return session, err == nil
}

func (s *Server) requireSession(r *http.Request) (identity.Session, error) {
	db := s.sqlDB()
	if db == nil {
		return identity.Session{}, identity.ErrSessionInvalid
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return identity.Session{}, identity.ErrSessionInvalid
	}
	return identity.New(db, identity.Options{}).VerifySession(r.Context(), cookie.Value)
}

func (s *Server) requireAIPrincipal(r *http.Request) (aitoken.Principal, error) {
	if s.sqlDB() == nil {
		return aitoken.Principal{}, aitoken.ErrInvalidToken
	}
	return aitoken.NewService(s.sqlDB()).VerifyBearer(r.Context(), r.Header.Get("Authorization"))
}

func (s *Server) searchAIPrincipal(r *http.Request, principal aitoken.Principal, query string, limit int) (catalog.SearchResult, error) {
	limit = parseIntDefault(strconv.Itoa(limit), 20)
	boundariesBySpace := map[string][]catalog.SearchBoundary{}
	for _, boundary := range principal.Boundaries {
		if !s.canReadSpace(r, principal.AccountID, boundary.SpaceID) || !s.mountBelongsToSpace(r, boundary.SpaceID, boundary.MountID) {
			return catalog.SearchResult{}, fmt.Errorf("AI token boundary is no longer authorized")
		}
		boundariesBySpace[boundary.SpaceID] = append(boundariesBySpace[boundary.SpaceID], catalog.SearchBoundary{
			MountID:      boundary.MountID,
			RelativePath: boundary.RelativePath,
		})
	}

	service := catalog.NewService(s.sqlDB())
	merged := catalog.SearchResult{}
	remaining := limit
	for spaceID, boundaries := range boundariesBySpace {
		if remaining <= 0 {
			break
		}
		result, err := service.Search(r.Context(), catalog.SearchOptions{
			SpaceID:    spaceID,
			Query:      query,
			Limit:      remaining,
			Boundaries: boundaries,
		})
		if err != nil {
			return catalog.SearchResult{}, err
		}
		merged.Items = append(merged.Items, result.Items...)
		merged.ExcludedMounts = append(merged.ExcludedMounts, result.ExcludedMounts...)
		remaining = limit - len(merged.Items)
	}
	return merged, nil
}

func (s *Server) canReadSpace(r *http.Request, accountID, spaceID string) bool {
	var count int
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT COUNT(1)
FROM space_members sm
JOIN spaces sp ON sp.id = sm.space_id
WHERE sm.account_id = ? AND sm.space_id = ? AND sp.status = 'active'
`, accountID, spaceID).Scan(&count)
	return err == nil && count == 1
}

type accountTOTP struct {
	Email      string
	Required   bool
	TOTPSecret string
}

func (s *Server) loadAccountForTOTP(r *http.Request, accountID string) (accountTOTP, error) {
	var account accountTOTP
	var required int
	var secret sql.NullString
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT email, totp_required, totp_secret_ciphertext
FROM accounts
WHERE id = ? AND status = 'active'
`, accountID).Scan(&account.Email, &required, &secret)
	if err != nil {
		return accountTOTP{}, err
	}
	account.Required = required == 1
	if secret.Valid {
		decrypted, err := s.decryptTOTPSecret(secret.String)
		if err != nil {
			return accountTOTP{}, err
		}
		account.TOTPSecret = decrypted
	}
	return account, nil
}

func (s *Server) verifyLoginTOTP(r *http.Request, accountID, code string) (bool, error) {
	account, err := s.loadAccountForTOTP(r, accountID)
	if err != nil {
		return false, err
	}
	if !account.Required {
		return true, nil
	}
	if account.TOTPSecret == "" {
		return false, nil
	}
	return totp.Verify(account.TOTPSecret, code, time.Now().UTC()), nil
}

func (s *Server) encryptTOTPSecret(secret string) (string, error) {
	aead, err := s.totpAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := crand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, []byte(secret), []byte("omnora-totp-v1"))
	return "v1:" + base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func (s *Server) decryptTOTPSecret(sealed string) (string, error) {
	parts := strings.Split(sealed, ":")
	if len(parts) != 3 || parts[0] != "v1" {
		return "", errors.New("TOTP secret requires encrypted storage")
	}
	aead, err := s.totpAEAD()
	if err != nil {
		return "", err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("TOTP secret ciphertext is invalid")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("TOTP secret ciphertext is invalid")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte("omnora-totp-v1"))
	if err != nil {
		return "", errors.New("TOTP secret ciphertext is invalid")
	}
	return string(plaintext), nil
}

func (s *Server) totpAEAD() (cipher.AEAD, error) {
	keyMaterial := strings.TrimSpace(s.cfg.Secrets.TOTPEncryptionKey)
	if len(keyMaterial) < 32 {
		return nil, errors.New("OMNORA_TOTP_ENCRYPTION_KEY must be set to at least 32 characters")
	}
	key := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Server) hasSpacePermission(r *http.Request, accountID, spaceID string, required domain.SpacePermission) bool {
	var permission domain.SpacePermission
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT sm.permission
FROM space_members sm
JOIN spaces sp ON sp.id = sm.space_id
WHERE sm.account_id = ? AND sm.space_id = ? AND sp.status = 'active'
`, accountID, spaceID).Scan(&permission)
	if err != nil {
		return false
	}
	if required == domain.SpacePermissionViewer {
		return accessRank(permission) >= accessRank(domain.SpacePermissionViewer)
	}
	if required == domain.SpacePermissionEditor {
		return accessRank(permission) >= accessRank(domain.SpacePermissionEditor)
	}
	return permission == domain.SpacePermissionManager
}

func (s *Server) isAdmin(r *http.Request, accountID string) bool {
	var count int
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT COUNT(1)
FROM accounts
WHERE id = ? AND role = 'admin' AND status = 'active'
`, accountID).Scan(&count)
	return err == nil && count == 1
}

func (s *Server) writeIdentityError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidInput):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, identity.ErrAlreadyInitialized), errors.Is(err, identity.ErrAccountExists):
		httpx.WriteError(w, r, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, identity.ErrInvalidCredential), errors.Is(err, identity.ErrInitializationUnavailable), errors.Is(err, identity.ErrSessionInvalid):
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "credentials are not valid")
	default:
		writeDBError(w, r, err)
	}
}

func writeTokenError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, aitoken.ErrInvalidInput), errors.Is(err, aitoken.ErrInvalidScope):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, aitoken.ErrInvalidToken):
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "AI token is not valid")
	default:
		writeDBError(w, r, err)
	}
}

var (
	errMountConflict             = errors.New("mount conflicts with an existing mount")
	errMountIdentityUnverifiable = errors.New("mount identity is unverifiable")
)

type validatedMount struct {
	SpaceName   string
	DisplayName string
	RootPath    string
	Kind        string
	Mode        domain.MountMode
	Identity    mountid.Identity
}

func (s *Server) validateMountRequest(r *http.Request, spaceID, displayName, rootPath, kind, mode string) (validatedMount, error) {
	spaceID = strings.TrimSpace(spaceID)
	displayName = strings.TrimSpace(displayName)
	rootPath = strings.TrimSpace(rootPath)
	if spaceID == "" || displayName == "" || rootPath == "" {
		return validatedMount{}, fmt.Errorf("spaceId, displayName, and rootPath are required")
	}
	if kind == "" {
		kind = "external"
	}
	if kind != "external" && kind != "managed" {
		return validatedMount{}, fmt.Errorf("mount kind must be external or managed")
	}
	mountMode := domain.MountMode(mode)
	if mountMode != domain.MountModeReadOnly && mountMode != domain.MountModeReadWrite {
		return validatedMount{}, fmt.Errorf("mount mode must be read_only or read_write")
	}
	var spaceName string
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT name
FROM spaces
WHERE id = ? AND status = 'active'
	`, spaceID).Scan(&spaceName)
	if errors.Is(err, sql.ErrNoRows) {
		return validatedMount{}, fmt.Errorf("space was not found")
	}
	if err != nil {
		return validatedMount{}, err
	}
	existing, err := s.loadMountIdentities(r)
	if err != nil {
		return validatedMount{}, err
	}
	identity, err := mountid.VerifyCandidateRoot(rootPath, existing)
	if err != nil {
		if errors.Is(err, mountid.ErrMountConflict) {
			return validatedMount{}, fmt.Errorf("%w: %v", errMountConflict, err)
		}
		return validatedMount{}, fmt.Errorf("%w: %v", errMountIdentityUnverifiable, err)
	}

	return validatedMount{SpaceName: spaceName, DisplayName: displayName, RootPath: identity.Path, Kind: kind, Mode: mountMode, Identity: identity}, nil
}

func (s *Server) loadMountIdentities(r *http.Request) ([]mountid.Identity, error) {
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT root_path, mount_identity_json
FROM mounts
WHERE status <> 'deleted'
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identities []mountid.Identity
	for rows.Next() {
		var rootPath, identityJSON string
		if err := rows.Scan(&rootPath, &identityJSON); err != nil {
			return nil, err
		}
		var identity mountid.Identity
		if strings.TrimSpace(identityJSON) != "" {
			if err := json.Unmarshal([]byte(identityJSON), &identity); err != nil {
				return nil, err
			}
		}
		if identity.Path == "" {
			captured, err := mountid.Capture(rootPath)
			if err != nil {
				return nil, err
			}
			identity = captured
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return false
	}
	return true
}

func writeDBError(w http.ResponseWriter, r *http.Request, err error) {
	_ = err
	httpx.WriteError(w, r, http.StatusInternalServerError, "internal_error", "request could not be completed")
}

type uploadRecord struct {
	ID        string
	TempDir   string
	SpaceID   string
	MountID   string
	PartSize  int
	ExpiresAt string
}

func (s *Server) loadUpload(r *http.Request, accountID, uploadID string) (uploadRecord, mountForListing, error) {
	var upload uploadRecord
	var mount mountForListing
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT u.id, u.temp_dir, u.space_id, u.mount_id, u.part_size, u.expires_at, m.id, m.root_path, m.mode, COALESCE(m.mount_identity_json, '')
FROM upload_sessions u
JOIN mounts m ON m.id = u.mount_id
WHERE u.id = ? AND u.account_id = ? AND u.status = 'active' AND u.expires_at > ? AND m.status = 'active'
	`, uploadID, accountID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(
		&upload.ID,
		&upload.TempDir,
		&upload.SpaceID,
		&upload.MountID,
		&upload.PartSize,
		&upload.ExpiresAt,
		&mount.ID,
		&mount.Root,
		&mount.Mode,
		&mount.IdentityJSON,
	)
	return upload, mount, err
}

func (s *Server) loadCatalogMount(r *http.Request, mountID string) (catalog.Mount, error) {
	var mount catalog.Mount
	var indexEnabled int
	var identity string
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT id, space_id, root_path, status, index_enabled, COALESCE(mount_identity_json, '')
FROM mounts
WHERE id = ? AND status <> 'deleted'
`, mountID).Scan(&mount.ID, &mount.SpaceID, &mount.Root, &mount.Status, &indexEnabled, &identity)
	if err != nil {
		return catalog.Mount{}, err
	}
	mount.IndexEnabled = indexEnabled == 1
	mount.IdentityVerified = identity != ""
	mount.IdentityJSON = identity
	return mount, nil
}

func pathJoinForUpload(parentPath, fileName string) string {
	parentPath = strings.Trim(strings.TrimSpace(parentPath), "/")
	fileName = strings.Trim(strings.TrimSpace(fileName), "/")
	if parentPath == "" {
		return fileName
	}
	return parentPath + "/" + fileName
}

func scopeIncluded(scopes []aitoken.Scope, want aitoken.Scope) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func (s *Server) mountBelongsToSpace(r *http.Request, spaceID, mountID string) bool {
	var count int
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT COUNT(1)
FROM mounts
WHERE id = ? AND space_id = ? AND status = 'active'
	`, mountID, spaceID).Scan(&count)
	return err == nil && count == 1
}

func (s *Server) mountAllowsUpload(r *http.Request, spaceID, mountID string) bool {
	var count int
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT COUNT(1)
FROM mounts
WHERE id = ? AND space_id = ? AND status = 'active' AND mode = 'read_write'
	`, mountID, spaceID).Scan(&count)
	return err == nil && count == 1
}

func (s *Server) aiTokenBoundaries(r *http.Request, tokenID string) ([]map[string]string, error) {
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT space_id, mount_id, relative_path
FROM ai_token_boundaries
WHERE token_id = ?
ORDER BY space_id, mount_id, relative_path
	`, tokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]string{}
	for rows.Next() {
		var spaceID, mountID, relativePath string
		if err := rows.Scan(&spaceID, &mountID, &relativePath); err != nil {
			return nil, err
		}
		items = append(items, map[string]string{"spaceId": spaceID, "mountId": mountID, "path": relativePath})
	}
	return items, rows.Err()
}

func parseBoundaryList(value string) []map[string]string {
	if value == "" {
		return []map[string]string{}
	}
	rows := strings.Split(value, string(rune(30)))
	items := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		parts := strings.Split(row, string(rune(31)))
		if len(parts) != 3 {
			continue
		}
		items = append(items, map[string]string{"spaceId": parts[0], "mountId": parts[1], "path": parts[2]})
	}
	return items
}

func aiTokenResponse(id, publicID, name, scopesJSON, createdAt, expiresAt, lastUsedAt, revokedAt string, boundaries []map[string]string) map[string]any {
	var scopes []string
	_ = json.Unmarshal([]byte(scopesJSON), &scopes)
	status := "active"
	tone := "ok"
	if revokedAt != "" {
		status = "revoked"
		tone = "muted"
	} else if expiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, expiresAt); err == nil && !time.Now().UTC().Before(parsed) {
			status = "expired"
			tone = "danger"
		}
	}
	return map[string]any{
		"id":         id,
		"publicId":   publicID,
		"name":       name,
		"scopes":     scopes,
		"boundaries": boundaries,
		"createdAt":  createdAt,
		"expiresAt":  expiresAt,
		"lastUsedAt": lastUsedAt,
		"revokedAt":  revokedAt,
		"status":     status,
		"tone":       tone,
	}
}

func jobResponse(job jobs.Job, claimedAt, claimedBy, lastError, completedAt string) map[string]any {
	return map[string]any{
		"id":          job.ID,
		"kind":        job.Kind,
		"priority":    job.Priority,
		"status":      job.Status,
		"payload":     json.RawMessage(job.PayloadJSON),
		"checkpoint":  json.RawMessage(job.CheckpointJSON),
		"attempts":    job.Attempts,
		"maxAttempts": job.MaxAttempts,
		"claimedAt":   claimedAt,
		"claimedBy":   claimedBy,
		"lastError":   lastError,
		"createdAt":   job.CreatedAt,
		"updatedAt":   job.UpdatedAt,
		"completedAt": completedAt,
	}
}

func parseIntDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func sessionCookie(r *http.Request, token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func shareSessionCookie(r *http.Request, token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     "omnora_share_session",
		Value:    token,
		Path:     "/share/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	}
}

func accountResponse(account identity.Account) map[string]string {
	return map[string]string{
		"id": account.ID, "email": account.Email, "displayName": account.DisplayName, "role": string(account.Role), "status": account.Status,
	}
}

func spaceResponse(space identity.Space, permission domain.SpacePermission) map[string]string {
	return map[string]string{
		"id": space.ID, "type": space.Kind, "name": space.Name, "role": string(permission),
	}
}

func requestCarriesShareSecretInURL(r *http.Request) bool {
	for _, key := range []string{"secret", "token", "key"} {
		if r.URL.Query().Get(key) != "" {
			return true
		}
	}
	referer := strings.ToLower(r.Header.Get("Referer"))
	return strings.Contains(referer, "secret=") || strings.Contains(referer, "token=") || strings.Contains(referer, "key=")
}

type mountForListing struct {
	ID           string
	Root         string
	Mode         domain.MountMode
	IdentityJSON string
}

func loadMountForListing(r *http.Request, db *sql.DB, spaceID, mountID string) (mountForListing, error) {
	var mount mountForListing
	err := db.QueryRowContext(r.Context(), `
SELECT id, root_path, mode, COALESCE(mount_identity_json, '')
FROM mounts
WHERE id = ? AND space_id = ? AND status = 'active'
	`, mountID, spaceID).Scan(&mount.ID, &mount.Root, &mount.Mode, &mount.IdentityJSON)
	return mount, err
}

func (s *Server) canContinueUpload(w http.ResponseWriter, r *http.Request, accountID, spaceID string, mount mountForListing) bool {
	if !s.hasSpacePermission(r, accountID, spaceID, domain.SpacePermissionEditor) {
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "upload requires current editor permission")
		return false
	}
	if mount.Mode != domain.MountModeReadWrite {
		httpx.WriteError(w, r, http.StatusForbidden, "readonly_mount", "mount is read-only")
		return false
	}
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
		return false
	}
	return true
}

func (s *Server) verifyLoadedMountIdentity(r *http.Request, mount mountForListing) error {
	if strings.TrimSpace(mount.IdentityJSON) == "" {
		_ = s.markMountUnavailable(r, mount.ID)
		return errMountIdentityUnverifiable
	}
	var stored mountid.Identity
	if err := json.Unmarshal([]byte(mount.IdentityJSON), &stored); err != nil {
		_ = s.markMountUnavailable(r, mount.ID)
		return fmt.Errorf("%w: %v", errMountIdentityUnverifiable, err)
	}
	current, err := mountid.Capture(mount.Root)
	if err != nil {
		_ = s.markMountUnavailable(r, mount.ID)
		return fmt.Errorf("%w: %v", errMountIdentityUnverifiable, err)
	}
	if !mountIdentityMatches(stored, current) {
		_ = s.markMountUnavailable(r, mount.ID)
		return fmt.Errorf("%w: mount root identity drifted", errMountIdentityUnverifiable)
	}
	return nil
}

func (s *Server) validateShareTarget(r *http.Request, mount mountForListing, requestedPath string) (string, error) {
	if err := s.verifyLoadedMountIdentity(r, mount); err != nil {
		return "", err
	}
	relativePath, err := files.NewService().ValidateShareTarget(files.Mount{Root: mount.Root, Mode: mount.Mode}, requestedPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("share target was not found")
	}
	if err != nil {
		return "", err
	}
	return relativePath, nil
}

func (s *Server) markMountUnavailable(r *http.Request, mountID string) error {
	if strings.TrimSpace(mountID) == "" {
		return nil
	}
	_, err := s.sqlDB().ExecContext(r.Context(), `
UPDATE mounts
SET status = 'unavailable', updated_at = ?
WHERE id = ? AND status = 'active'
	`, time.Now().UTC().Format(time.RFC3339Nano), mountID)
	return err
}

func mountIdentityMatches(stored, current mountid.Identity) bool {
	if filepath.Clean(stored.Path) != filepath.Clean(current.Path) || stored.Device != current.Device || stored.Inode != current.Inode {
		return false
	}
	if stored.Statx.Available || current.Statx.Available {
		if stored.Statx.Available != current.Statx.Available ||
			stored.Statx.DeviceMajor != current.Statx.DeviceMajor ||
			stored.Statx.DeviceMinor != current.Statx.DeviceMinor ||
			stored.Statx.Inode != current.Statx.Inode ||
			stored.Statx.MountID != current.Statx.MountID {
			return false
		}
	}
	if stored.Mount.Available != current.Mount.Available {
		return false
	}
	if !stored.Mount.Available {
		return true
	}
	return stored.Mount.ID == current.Mount.ID &&
		stored.Mount.Device == current.Mount.Device &&
		stored.Mount.Root == current.Mount.Root &&
		stored.Mount.Point == current.Mount.Point &&
		stored.Mount.FSType == current.Mount.FSType &&
		stored.Mount.Source == current.Mount.Source
}

func queryMounts(r *http.Request, db *sql.DB, accountID, spaceID string) ([]mountDTO, error) {
	rows, err := db.QueryContext(r.Context(), `
SELECT m.id, m.display_name, sp.name, m.mode, m.index_enabled, m.status
FROM mounts m
JOIN spaces sp ON sp.id = m.space_id
JOIN space_members sm ON sm.space_id = sp.id
WHERE sm.account_id = ? AND sp.id = ? AND sp.status = 'active' AND m.status <> 'deleted'
ORDER BY m.display_name
`, accountID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMountDTOs(rows)
}

func (s *Server) bootstrapMounts(r *http.Request, db *sql.DB, session identity.Session, authenticated bool) []mountDTO {
	if !authenticated {
		return []mountDTO{}
	}
	rows, err := db.QueryContext(r.Context(), `
SELECT m.id, m.display_name, sp.name, m.mode, m.index_enabled, m.status
FROM mounts m
JOIN spaces sp ON sp.id = m.space_id
JOIN space_members sm ON sm.space_id = sp.id
WHERE sm.account_id = ? AND sp.status = 'active' AND m.status <> 'deleted'
ORDER BY sp.kind, sp.name, m.display_name
`, session.AccountID)
	if err != nil {
		return []mountDTO{{Name: "mount query failed", Space: "system", Mode: "read-only", Index: "unknown", Health: "error", Tone: "danger"}}
	}
	defer rows.Close()
	items, err := scanMountDTOs(rows)
	if err != nil {
		return []mountDTO{{Name: "mount query failed", Space: "system", Mode: "read-only", Index: "unknown", Health: "error", Tone: "danger"}}
	}
	return items
}

func scanMountDTOs(rows *sql.Rows) ([]mountDTO, error) {
	items := []mountDTO{}
	for rows.Next() {
		var item mountDTO
		var mode string
		var indexEnabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.Space, &mode, &indexEnabled, &item.Health); err != nil {
			return nil, err
		}
		item.Mode = displayMountMode(domain.MountMode(mode))
		item.Index = "not indexed"
		if indexEnabled == 1 {
			item.Index = "indexed"
		}
		item.Tone = toneForStatus(item.Health)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Server) bootstrapFiles(r *http.Request, db *sql.DB, session identity.Session, authenticated bool) []fileDTO {
	if !authenticated {
		return []fileDTO{}
	}
	var mountID, mountName, rootPath, mode string
	var indexEnabled int
	err := db.QueryRowContext(r.Context(), `
SELECT m.id, m.display_name, m.root_path, m.mode, m.index_enabled
FROM mounts m
JOIN spaces sp ON sp.id = m.space_id
JOIN space_members sm ON sm.space_id = sp.id
WHERE sm.account_id = ? AND sp.status = 'active' AND m.status = 'active'
ORDER BY sp.kind, sp.name, m.display_name
LIMIT 1
`, session.AccountID).Scan(&mountID, &mountName, &rootPath, &mode, &indexEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return []fileDTO{}
	}
	if err != nil {
		return []fileDTO{{ID: "files-error", Name: "File query failed", Kind: "archive", Size: "0", Modified: "", Mount: "system", Access: "read-only", Status: "error", StatusTone: "danger", Index: "unknown"}}
	}
	listing, err := files.NewService().ListDirectory(files.Mount{Root: rootPath, Mode: domain.MountMode(mode)}, ".")
	if err != nil {
		return []fileDTO{{ID: "files-error", Name: err.Error(), Kind: "archive", Size: "0", Modified: "", Mount: mountName, Access: displayMountMode(domain.MountMode(mode)), Status: "unavailable", StatusTone: "danger", Index: displayIndex(indexEnabled)}}
	}
	items := make([]fileDTO, 0, len(listing.Entries))
	for i, entry := range listing.Entries {
		items = append(items, mapFileEntry(fmt.Sprintf("%s:%d", mountID, i), mountName, displayIndex(indexEnabled), entry))
	}
	return items
}

func mapFileEntry(id, mountName, index string, entry files.Entry) fileDTO {
	status := "available"
	statusTone := "ok"
	if entry.PreviewKind == files.PreviewKindOfficeDownload {
		status = "download only"
		statusTone = "warn"
	}
	return fileDTO{
		ID: id, Name: entry.Name, Kind: displayFileKind(entry), Size: displaySize(entry), Modified: entry.ModifiedAt.Format("Jan 2, 2006 15:04"),
		Mount: mountName, Access: displayAccess(entry.ReadOnly), Status: status, StatusTone: statusTone, Index: index,
	}
}

func (s *Server) bootstrapShares(r *http.Request, db *sql.DB, session identity.Session, authenticated bool) []shareDTO {
	if !authenticated {
		return []shareDTO{}
	}
	rows, err := db.QueryContext(r.Context(), `
SELECT public_id, relative_path, allow_preview, allow_download, max_visits, used_visits, expires_at, revoked_at
FROM shares
WHERE creator_account_id = ?
ORDER BY created_at DESC
LIMIT 20
`, session.AccountID)
	if err != nil {
		return []shareDTO{{ID: "shares-error", Target: "share query failed", Capability: "metadata only", Expires: "unknown", Use: "0 / 0 visits", Status: "error", Tone: "danger"}}
	}
	defer rows.Close()
	items := []shareDTO{}
	for rows.Next() {
		var publicID, relativePath, expiresAt string
		var revokedAt sql.NullString
		var allowPreview, allowDownload, usedVisits int
		var maxVisits sql.NullInt64
		if err := rows.Scan(&publicID, &relativePath, &allowPreview, &allowDownload, &maxVisits, &usedVisits, &expiresAt, &revokedAt); err != nil {
			return []shareDTO{{ID: "shares-error", Target: "share query failed", Capability: "metadata only", Expires: "unknown", Use: "0 / 0 visits", Status: "error", Tone: "danger"}}
		}
		items = append(items, shareDTO{
			ID: publicID, Target: relativePath, Capability: displayCapability(allowPreview, allowDownload),
			Expires: expiresAt, Use: displayUses(usedVisits, maxVisits), Status: displayShareStatus(revokedAt), Tone: toneForShare(revokedAt),
		})
	}
	return items
}

func (s *Server) bootstrapTokens(r *http.Request, db *sql.DB, session identity.Session, authenticated bool) []tokenDTO {
	if !authenticated {
		return []tokenDTO{}
	}
	rows, err := db.QueryContext(r.Context(), `
SELECT t.id, t.name, t.scopes, COALESCE(t.revoked_at, ''),
       COALESCE((
	       SELECT group_concat(space_id || '/' || mount_id || '/' || relative_path, ', ')
	       FROM ai_token_boundaries
	       WHERE token_id = t.id
       ), '')
FROM ai_tokens t
WHERE t.account_id = ?
ORDER BY t.created_at DESC
LIMIT 20
	`, session.AccountID)
	if err != nil {
		return []tokenDTO{{ID: "tokens-error", Name: "Token query failed", Scope: "", Boundary: "", Status: "error", Tone: "danger"}}
	}
	defer rows.Close()

	items := []tokenDTO{}
	for rows.Next() {
		var item tokenDTO
		var scopesJSON, revokedAt string
		if err := rows.Scan(&item.ID, &item.Name, &scopesJSON, &revokedAt, &item.Boundary); err != nil {
			return []tokenDTO{{ID: "tokens-error", Name: "Token query failed", Scope: "", Boundary: "", Status: "error", Tone: "danger"}}
		}
		var scopes []string
		_ = json.Unmarshal([]byte(scopesJSON), &scopes)
		item.Scope = strings.Join(scopes, ", ")
		item.Status = "active"
		item.Tone = "ok"
		if revokedAt != "" {
			item.Status = "revoked"
			item.Tone = "muted"
		}
		items = append(items, item)
	}
	return items
}

func (s *Server) bootstrapAudit(r *http.Request, db *sql.DB) []auditDTO {
	rows, err := db.QueryContext(r.Context(), `
SELECT occurred_at, COALESCE(actor_account_id, 'system'), action, target_type, COALESCE(target_id, '')
FROM audit_events
ORDER BY id DESC
LIMIT 20
`)
	if err != nil {
		return []auditDTO{{Time: "", Actor: "system", Event: "audit_query_failed", Target: "audit", Result: "error"}}
	}
	defer rows.Close()
	items := []auditDTO{}
	for rows.Next() {
		var item auditDTO
		var targetType, targetID string
		if err := rows.Scan(&item.Time, &item.Actor, &item.Event, &targetType, &targetID); err != nil {
			return []auditDTO{{Time: "", Actor: "system", Event: "audit_query_failed", Target: "audit", Result: "error"}}
		}
		item.Target = strings.TrimSpace(targetType + " " + targetID)
		item.Result = "recorded"
		items = append(items, item)
	}
	return items
}

func (s *Server) bootstrapRisks(db *sql.DB) []adminRiskDTO {
	var accountCount, mountCount, shareCount int
	_ = db.QueryRow("SELECT COUNT(1) FROM accounts WHERE status = 'active'").Scan(&accountCount)
	_ = db.QueryRow("SELECT COUNT(1) FROM mounts WHERE status <> 'deleted'").Scan(&mountCount)
	_ = db.QueryRow("SELECT COUNT(1) FROM shares WHERE revoked_at IS NULL").Scan(&shareCount)
	return []adminRiskDTO{
		{Label: "Active accounts", Value: strconv.Itoa(accountCount), Tone: riskTone(accountCount > 0)},
		{Label: "Registered mounts", Value: strconv.Itoa(mountCount), Tone: riskTone(mountCount > 0)},
		{Label: "Active shares", Value: strconv.Itoa(shareCount), Tone: "info"},
		{Label: "Database", Value: "WAL ready", Tone: "ok"},
	}
}

func (s *Server) recordAudit(r *http.Request, action, targetType, targetID, metadata string) error {
	db := s.sqlDB()
	if db == nil {
		return nil
	}
	session, _ := s.optionalSession(r)
	event := audit.Event{
		ActorAccountID: session.AccountID,
		RouteGroup:     domain.RouteGroupREST,
		Action:         action,
		TargetType:     targetType,
		TargetID:       targetID,
		MetadataJSON:   metadata,
		IPHash:         audit.HashForAudit(r.RemoteAddr),
		UserAgentHash:  audit.HashForAudit(r.UserAgent()),
	}
	return audit.NewRecorder(db).Record(r.Context(), event)
}

func displayMountMode(mode domain.MountMode) string {
	if mode == domain.MountModeReadWrite {
		return "read-write"
	}
	return "read-only"
}

func displayAccess(readOnly bool) string {
	if readOnly {
		return "read-only"
	}
	return "read-write"
}

func displayIndex(indexEnabled int) string {
	if indexEnabled == 1 {
		return "included"
	}
	return "excluded"
}

func displaySize(entry files.Entry) string {
	if entry.Kind == files.EntryKindDir {
		return "folder"
	}
	if entry.Size < 1024 {
		return fmt.Sprintf("%d B", entry.Size)
	}
	if entry.Size < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(entry.Size)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(entry.Size)/(1024*1024))
}

func displayFileKind(entry files.Entry) string {
	if entry.Kind == files.EntryKindDir {
		return "folder"
	}
	switch entry.PreviewKind {
	case files.PreviewKindImage:
		return "image"
	case files.PreviewKindPDF:
		return "pdf"
	case files.PreviewKindMarkdown:
		return "markdown"
	case files.PreviewKindText:
		return "markdown"
	case files.PreviewKindMedia:
		return "audio"
	case files.PreviewKindOfficeDownload:
		return "office"
	default:
		return "archive"
	}
}

func displayCapability(allowPreview, allowDownload int) string {
	switch {
	case allowPreview == 1 && allowDownload == 1:
		return "preview + download"
	case allowPreview == 1:
		return "preview only"
	case allowDownload == 1:
		return "download"
	default:
		return "metadata only"
	}
}

func displayUses(used int, max sql.NullInt64) string {
	if max.Valid {
		return fmt.Sprintf("%d / %d visits", used, max.Int64)
	}
	return fmt.Sprintf("%d / unlimited visits", used)
}

func displayShareStatus(revokedAt sql.NullString) string {
	if revokedAt.Valid {
		return "revoked"
	}
	return "active"
}

func toneForShare(revokedAt sql.NullString) string {
	if revokedAt.Valid {
		return "danger"
	}
	return "ok"
}

func toneForStatus(status string) string {
	switch status {
	case "active":
		return "ok"
	case "pending", "disabled":
		return "warn"
	case "unavailable":
		return "danger"
	default:
		return "muted"
	}
}

func riskTone(ok bool) string {
	if ok {
		return "ok"
	}
	return "warn"
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func accessRank(permission domain.SpacePermission) int {
	switch permission {
	case domain.SpacePermissionViewer:
		return 1
	case domain.SpacePermissionEditor:
		return 2
	case domain.SpacePermissionManager:
		return 3
	default:
		return 0
	}
}
