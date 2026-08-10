package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/audit"
	"omnora/internal/catalog"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/jobs"
	"omnora/internal/memberfiles"
	"omnora/internal/membershare"
	"omnora/internal/mountadmin"
	"omnora/internal/mountid"
	"omnora/internal/ratelimit"
	"omnora/internal/share"
	"omnora/internal/totp"
)

const sessionCookieName = "omnora_dev_session"

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

type auditEventDTO struct {
	OccurredAt       string `json:"occurredAt"`
	Actor            string `json:"actor"`
	ActorEmail       string `json:"actorEmail,omitempty"`
	ActorDisplayName string `json:"actorDisplayName,omitempty"`
	ActorLabel       string `json:"actorLabel"`
	RouteGroup       string `json:"routeGroup"`
	Action           string `json:"action"`
	TargetType       string `json:"targetType"`
	TargetID         string `json:"targetId"`
	TargetLabel      string `json:"targetLabel"`
	Metadata         string `json:"metadata"`
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
	InitializationAvailable bool            `json:"initializationAvailable"`
	RouteGroups             []routeGroupDTO `json:"routeGroups"`
	Mounts                  []mountDTO      `json:"mounts"`
	Files                   []fileDTO       `json:"files"`
	Transfers               []transferDTO   `json:"transfers"`
	AdminRisks              []adminRiskDTO  `json:"adminRisks"`
	AuditRows               []auditDTO      `json:"auditRows"`
	Shares                  []shareDTO      `json:"shares"`
	Tokens                  []tokenDTO      `json:"tokens"`
}

func (s *Server) apiRoutes() {
	s.mux.Handle("GET /api/v1/bootstrap", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.bootstrap)))
	s.mux.Handle("POST /api/v1/initialize", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.initialize)))
	s.mux.Handle("GET /api/v1/auth/session", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.currentSession)))
	s.mux.Handle("POST /api/v1/auth/session", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createSession)))
	s.mux.Handle("DELETE /api/v1/auth/session", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeSession)))
	s.mux.Handle("POST /api/v1/account/reauthenticate", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.reauthenticateAccount)))
	s.mux.Handle("POST /api/v1/account/totp/setup", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.setupTOTP)))
	s.mux.Handle("POST /api/v1/account/totp/confirm", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.confirmTOTP)))
	s.mux.Handle("GET /api/v1/member/content-sources", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listMemberContentSources)))
	s.mux.Handle("GET /api/v1/member/files/children", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listMemberFileChildren)))
	s.mux.Handle("GET /api/v1/member/files/download", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberDownload)))
	s.mux.Handle("GET /api/v1/member/files/search", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberSearch)))
	s.mux.Handle("POST /api/v1/member/files/directories", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberCreateDirectory)))
	s.mux.Handle("POST /api/v1/member/files/rename", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberRename)))
	s.mux.Handle("POST /api/v1/member/files/move", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberMove)))
	s.mux.Handle("POST /api/v1/member/files/copy", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberCopy)))
	s.mux.Handle("DELETE /api/v1/member/files/object", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberDelete)))
	s.mux.Handle("GET /api/v1/member/files/trash", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberTrashList)))
	s.mux.Handle("DELETE /api/v1/member/files/trash", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberTrashEmpty)))
	s.mux.Handle("POST /api/v1/member/files/trash/{trashId}/restore", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberTrashRestore)))
	s.mux.Handle("DELETE /api/v1/member/files/trash/{trashId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.memberTrashPurge)))
	s.mux.Handle("POST /api/v1/member/uploads", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createUpload)))
	s.mux.Handle("GET /api/v1/member/uploads/{uploadId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.getUpload)))
	s.mux.Handle("PUT /api/v1/member/uploads/{uploadId}/parts/{partNumber}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.uploadPart)))
	s.mux.Handle("POST /api/v1/member/uploads/{uploadId}/complete", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.completeUpload)))
	s.mux.Handle("DELETE /api/v1/member/uploads/{uploadId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.cancelUpload)))
	s.mux.Handle("GET /api/v1/member/collaborations", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listMemberCollaborationsV2)))
	s.mux.Handle("POST /api/v1/member/collaborations", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createMemberCollaboration)))
	s.mux.Handle("PATCH /api/v1/member/collaborations/{collaborationId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.updateMemberCollaboration)))
	s.mux.Handle("DELETE /api/v1/member/collaborations/{collaborationId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeMemberCollaboration)))
	s.mux.Handle("GET /api/v1/account", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.getAccount)))
	s.mux.Handle("PATCH /api/v1/account/password", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.changeAccountPassword)))
	s.mux.Handle("GET /api/v1/account/sessions", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAccountSessions)))
	s.mux.Handle("DELETE /api/v1/account/sessions/{sessionId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeAccountSession)))
	s.mux.Handle("POST /api/v1/account/totp/disable", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.disableAccountTOTP)))
	s.mux.Handle("GET /api/v1/account/preferences", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.getAccountPreferences)))
	s.mux.Handle("PUT /api/v1/account/preferences", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.putAccountPreferences)))
	s.mux.Handle("GET /api/v1/shares", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listShares)))
	s.mux.Handle("POST /api/v1/shares", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createShare)))
	s.mux.Handle("DELETE /api/v1/shares/{shareId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeShare)))
	s.mux.Handle("GET /api/v1/share/current", s.gate(domain.RouteGroupShare, http.HandlerFunc(s.shareCurrent)))
	s.mux.Handle("GET /api/v1/share/children", s.gate(domain.RouteGroupShare, http.HandlerFunc(s.shareChildren)))
	s.mux.Handle("GET /api/v1/share/download", s.gate(domain.RouteGroupShare, http.HandlerFunc(s.shareDownload)))
	s.mux.Handle("GET /api/v1/admin/overview", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminOverview)))
	s.mux.Handle("GET /api/v1/audit/events", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAuditEvents)))
	s.mux.Handle("GET /api/v1/admin/users", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminUsers)))
	s.mux.Handle("POST /api/v1/admin/users", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createAdminUser)))
	s.mux.Handle("POST /api/v1/admin/users/{userId}/disable", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.disableAdminUser)))
	s.mux.Handle("POST /api/v1/admin/users/{userId}/enable", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.enableAdminUser)))
	s.mux.Handle("POST /api/v1/admin/users/{userId}/revoke-sessions", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeAdminUserSessions)))
	s.mux.Handle("GET /api/v1/admin/ai-tokens", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminAITokens)))
	s.mux.Handle("DELETE /api/v1/admin/ai-tokens/{tokenId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeAdminAIToken)))
	s.mux.Handle("GET /api/v1/admin/backups", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listBackups)))
	s.mux.Handle("POST /api/v1/admin/backups", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createBackup)))
	s.mux.Handle("GET /api/v1/admin/recovery", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.recoveryStatus)))
	s.mux.Handle("POST /api/v1/admin/backups/{backupId}/restore", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.restoreBackup)))
	s.mux.Handle("GET /api/v1/admin/shares", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminShares)))
	s.mux.Handle("DELETE /api/v1/admin/shares/{shareId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeAdminShare)))
	s.mux.Handle("GET /api/v1/admin/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminListMounts)))
	s.mux.Handle("POST /api/v1/admin/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminCreateMount)))
	s.mux.Handle("PATCH /api/v1/admin/mounts/{mountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminUpdateMount)))
	s.mux.Handle("DELETE /api/v1/admin/mounts/{mountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminDeleteMount)))
	s.mux.Handle("POST /api/v1/admin/mounts/{mountId}/reverify", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminReverifyMount)))
	s.mux.Handle("GET /api/v1/admin/mounts/{mountId}/grants", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminListMountGrants)))
	s.mux.Handle("PUT /api/v1/admin/mounts/{mountId}/grants/{accountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminPutMountGrant)))
	s.mux.Handle("DELETE /api/v1/admin/mounts/{mountId}/grants/{accountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminDeleteMountGrant)))
	s.mux.Handle("GET /api/v1/admin/host-directories", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminHostDirectories)))
	s.mux.Handle("GET /api/v1/admin/index-jobs", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listIndexJobs)))
	s.mux.Handle("POST /api/v1/admin/index-jobs", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.enqueueIndexJob)))
	s.mux.Handle("POST /api/v1/admin/index-jobs/{jobId}/run", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.runIndexJob)))
	s.registerOpenAPIRoutes()
	s.mux.Handle("GET /api/v1/ai-tokens", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAITokens)))
	s.mux.Handle("POST /api/v1/ai-tokens", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createAIToken)))
	s.mux.Handle("DELETE /api/v1/ai-tokens/{tokenId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeAIToken)))
	s.mux.Handle("GET /api/v1/admin/route-groups", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminRouteGroups)))
	s.mux.Handle("PATCH /api/v1/admin/route-groups/{groupId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.updateAdminRouteGroup)))
	s.mux.Handle("POST /api/v1/share-sessions", s.gate(domain.RouteGroupShare, http.HandlerFunc(s.createShareSession)))
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	payload := bootstrapDTO{
		InitializationAvailable: false,
		RouteGroups:             s.routeGroups(),
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
	payload.InitializationAvailable = s.initializationAvailable(r.Context(), db)

	session, authenticated := s.optionalSession(r)
	payload.Mounts = []mountDTO{}
	payload.Files = []fileDTO{}
	payload.Shares = []shareDTO{}
	if authenticated && s.isAdmin(r, session.AccountID) {
		payload.AuditRows = []auditDTO{}
		payload.AdminRisks = s.bootstrapRisks(db)
	} else if authenticated {
		payload.AdminRisks = []adminRiskDTO{{Label: "Session", Value: "authenticated", Tone: "ok"}}
	} else {
		payload.AdminRisks = []adminRiskDTO{{Label: "Session", Value: "not authenticated", Tone: "muted"}}
	}
	httpx.WriteJSON(w, http.StatusOK, payload)
}

func (s *Server) initializationAvailable(ctx context.Context, db *sql.DB) bool {
	var accountCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM accounts
WHERE status <> 'deleted'
`).Scan(&accountCount); err != nil || accountCount != 0 {
		return false
	}

	var pendingCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM identity_initialization
WHERE id = 1 AND consumed_at IS NULL AND expires_at > ?
`, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&pendingCount); err != nil {
		return false
	}
	return pendingCount == 1
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
	// Initialization is a single credential bucket. Keying by caller-supplied
	// email/token would let an attacker rotate subjects to evade the limit.
	initSubject := "initialization"
	if decision := s.checkCredentialRateLimit(r, ratelimit.ScopeInitialize, initSubject); !decision.Allowed {
		writeRateLimited(w, r, decision)
		return
	}
	created, err := identity.New(db, identity.Options{ManagedDir: s.cfg.Storage.ManagedDir}).InitializeSecure(r.Context(), identity.InitializationRequest{
		Token:       req.Token,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
	}, func(ctx context.Context, tx *sql.Tx, created identity.AccountWithPersonalDirectory) error {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO system_state(key, value, updated_at)
VALUES ('initial_admin_account_id', ?, ?)
ON CONFLICT(key) DO NOTHING
`, created.Account.ID, nowRFC3339()); err != nil {
			return err
		}
		return s.recordAuditTx(ctx, tx, r, "", "system_initialize", "account", created.Account.ID, "{}")
	})
	if err != nil {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeInitialize, initSubject)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		s.writeIdentityError(w, r, err)
		return
	}
	s.recordCredentialSuccess(r, ratelimit.ScopeInitialize, initSubject)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"user":              accountResponse(created.Account),
		"personalDirectory": created.PersonalDirectory,
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
	if decision := s.checkCredentialRateLimit(r, ratelimit.ScopeLogin, req.Login); !decision.Allowed {
		writeRateLimited(w, r, decision)
		return
	}
	svc := identity.New(db, identity.Options{})
	account, err := svc.Authenticate(r.Context(), req.Login, req.Password)
	if err != nil {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeLogin, req.Login)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
		return
	}
	// TOTP is optional for every role, including administrators. Only
	// accounts that have already enabled TOTP must supply a code at login.
	if ok, err := s.verifyLoginTOTP(r, account.ID, req.TOTPCode); err != nil {
		writeDBError(w, r, err)
		return
	} else if !ok {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeLogin, req.Login)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
		return
	}
	issued, err := svc.CreateSession(r.Context(), identity.SessionRequest{
		AccountID: account.ID,
		TTL:       8 * time.Hour,
		Purpose:   identity.SessionPurposeFull,
	})
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	s.recordCredentialSuccess(r, ratelimit.ScopeLogin, req.Login)
	if !s.recordAuditMutation(w, r, "login", "account", account.ID, "{}") {
		return
	}
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if s.httpPolicy != nil {
		if _, err := SetCSRFCookie(w, s.cookieNames(r).CSRF, isSecureRequest(r), time.Now().UTC()); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	http.SetCookie(w, s.sessionCookie(r, issued.Token, issued.Session.ExpiresAt))
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"userId":                   account.ID,
		"expiresAt":                issued.Session.ExpiresAt,
		"isAdmin":                  s.isAdmin(r, account.ID),
		"isInitialAdmin":           initialAdminID == account.ID,
		"purpose":                  issued.Session.Purpose,
		"requiresTotpEnrollment":   issued.Session.Purpose == identity.SessionPurposeTOTPEnrollment,
		"passwordResetRecommended": account.PasswordResetRequired,
	})
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireAuthenticatedSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"userId":                 session.AccountID,
		"expiresAt":              session.ExpiresAt,
		"isAdmin":                s.isAdmin(r, session.AccountID),
		"isInitialAdmin":         initialAdminID == session.AccountID,
		"purpose":                session.Purpose,
		"requiresTotpEnrollment": session.Purpose == identity.SessionPurposeTOTPEnrollment,
	})
}

// reauthenticateAccount verifies the current credential set and rotates the
// session without changing its absolute expiry. The replacement session is
// the only row carrying the short-lived recent-reauthentication marker.
func (s *Server) reauthenticateAccount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireAuthenticatedSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
		return
	}
	if session.Purpose != identity.SessionPurposeFull {
		httpx.WriteError(w, r, http.StatusForbidden, "enrollment_session", "TOTP enrollment sessions cannot reauthenticate")
		return
	}
	var req struct {
		Password string `json:"password"`
		TOTPCode string `json:"totpCode"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if decision := s.checkCredentialRateLimit(r, ratelimit.ScopeReauthenticate, session.AccountID); !decision.Allowed {
		writeRateLimited(w, r, decision)
		return
	}
	svc := identity.New(s.sqlDB(), identity.Options{})
	material, err := svc.LoadCredentialMaterial(r.Context(), session.AccountID)
	if err != nil || !svc.VerifyPassword(req.Password, material.PasswordHash) {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeReauthenticate, session.AccountID)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
		return
	}
	if material.TOTPRequired {
		secret, decryptErr := s.decryptTOTPSecret(material.TOTPSecretCiphertext)
		if decryptErr != nil || secret == "" || !totp.Verify(secret, req.TOTPCode, time.Now().UTC()) {
			decision := s.recordCredentialFailure(r, ratelimit.ScopeReauthenticate, session.AccountID)
			if !decision.Allowed {
				writeRateLimited(w, r, decision)
				return
			}
			httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
			return
		}
	}
	now := time.Now().UTC()
	rotated, err := svc.RotateSessionSecure(r.Context(), session, now, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "account_reauthenticate", "account", session.AccountID, "{}")
	})
	if err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		s.writeIdentityError(w, r, err)
		return
	}
	s.recordCredentialSuccess(r, ratelimit.ScopeReauthenticate, session.AccountID)
	until := rotated.Session.ReauthenticatedAt.Add(identity.RecentReauthenticationTTL)
	if until.After(rotated.Session.ExpiresAt) {
		until = rotated.Session.ExpiresAt
	}
	if s.httpPolicy != nil {
		if _, err := SetCSRFCookie(w, s.cookieNames(r).CSRF, isSecureRequest(r), time.Now().UTC()); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	http.SetCookie(w, s.sessionCookie(r, rotated.Token, rotated.Session.ExpiresAt))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":               "reauthenticated",
		"reauthenticatedUntil": until,
	})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	db := s.requireDB(w, r)
	if db == nil {
		return
	}
	cookie, err := r.Cookie(s.cookieNames(r).Session)
	if err != nil || cookie.Value == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := identity.New(db, identity.Options{}).RevokeSession(r.Context(), cookie.Value); err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setupTOTP(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireAuthenticatedSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	var email string
	err = s.sqlDB().QueryRowContext(r.Context(), `
SELECT email
FROM accounts
WHERE id = ? AND status = 'active'
`, session.AccountID).Scan(&email)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	uri, err := totp.OTPAuthURI(totp.URIOptions{
		Issuer:      "Omnora",
		AccountName: email,
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
	if err := identity.New(s.sqlDB(), identity.Options{}).SavePendingTOTPSecure(
		r.Context(), session.AccountID, sealedSecret, time.Now().UTC().Add(identity.PendingTOTPDuration),
		func(ctx context.Context, tx *sql.Tx) error {
			return s.recordAuditTx(ctx, tx, r, session.AccountID, "totp_setup", "account", session.AccountID, "{}")
		},
	); err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"secret":     secret,
		"otpauthUri": uri,
	})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireAuthenticatedSession(r)
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
	if decision := s.checkCredentialRateLimit(r, ratelimit.ScopeTOTP, session.AccountID); !decision.Allowed {
		writeRateLimited(w, r, decision)
		return
	}
	rejectTOTP := func() {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeTOTP, session.AccountID)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_totp", "TOTP code is not valid")
	}
	svc := identity.New(s.sqlDB(), identity.Options{})
	state, err := svc.LoadTOTPState(r.Context(), session.AccountID)
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCredential) {
			httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
			return
		}
		writeDBError(w, r, err)
		return
	}
	now := time.Now().UTC()
	if state.PendingCiphertext == "" || state.PendingExpiresAt.IsZero() || !now.Before(state.PendingExpiresAt) {
		rejectTOTP()
		return
	}
	pendingSecret, err := s.decryptTOTPSecret(state.PendingCiphertext)
	if err != nil || pendingSecret == "" || !totp.Verify(pendingSecret, req.Code, now) {
		rejectTOTP()
		return
	}
	rotated, err := svc.PromotePendingTOTPAndRotateSessionSecure(r.Context(), session, state.PendingCiphertext, now, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.recordAuditTx(ctx, tx, r, session.AccountID, "totp_confirm", "account", session.AccountID, "{}"); err != nil {
			return audit.WrapWriteError(err)
		}
		return nil
	})
	if err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		if errors.Is(err, identity.ErrInvalidCredential) || errors.Is(err, identity.ErrSessionInvalid) {
			rejectTOTP()
			return
		}
		writeDBError(w, r, err)
		return
	}
	s.recordCredentialSuccess(r, ratelimit.ScopeTOTP, session.AccountID)
	if s.httpPolicy != nil {
		if _, err := SetCSRFCookie(w, s.cookieNames(r).CSRF, isSecureRequest(r), time.Now().UTC()); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	http.SetCookie(w, s.sessionCookie(r, rotated.Token, rotated.Session.ExpiresAt))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "enabled"})
}

func (s *Server) listAdminHostDirectories(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}

	suggestions, err := s.suggestHostDirectories(r.URL.Query().Get("path"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, suggestions)
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		Source          string `json:"source"`
		MountID         string `json:"mountId"`
		CollaborationID string `json:"collaborationId"`
		ParentPath      string `json:"parentPath"`
		FileName        string `json:"fileName"`
		Size            int64  `json:"size"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	targetPath := pathJoinForUpload(req.ParentPath, req.FileName)
	locator, err := memberLocatorFromJSON(req.Source, req.MountID, req.CollaborationID, targetPath)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_locator", err.Error())
		return
	}
	result, err := s.memberFiles.PrepareUpload(r.Context(), access.Subject{AccountID: session.AccountID}, memberfiles.UploadRequest{
		Locator: locator, ExpectedSize: req.Size,
	})
	if err != nil {
		writeMemberFilesError(w, r, err, "upload requires editor permission")
		return
	}
	if !s.recordAuditMutation(w, r, "upload_create", "upload", result.ID, "{}") {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": result.ID, "expiresAt": result.ExpiresAt, "partSize": result.PartSize})
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	result, err := s.memberFiles.UploadStatus(r.Context(), access.Subject{AccountID: session.AccountID}, r.PathValue("uploadId"))
	if err != nil {
		writeMemberFilesError(w, r, err, "upload requires current editor permission")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id":           result.ID,
		"targetPath":   result.TargetPath,
		"expectedSize": result.ExpectedSize,
		"receivedSize": result.ReceivedSize,
		"parts":        result.Parts,
		"partSize":     result.PartSize,
		"expiresAt":    result.ExpiresAt,
	})
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	partNumber, err := strconv.Atoi(r.PathValue("partNumber"))
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "part number is invalid")
		return
	}
	err = s.memberFiles.WriteUploadPart(r.Context(), access.Subject{AccountID: session.AccountID}, r.PathValue("uploadId"), partNumber, r.Body)
	if err != nil {
		writeMemberFilesError(w, r, err, "upload requires current editor permission")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	result, err := s.memberFiles.CompleteUpload(r.Context(), access.Subject{AccountID: session.AccountID}, r.PathValue("uploadId"))
	if err != nil {
		writeMemberFilesError(w, r, err, "upload requires current editor permission")
		return
	}
	if !s.recordAuditMutation(w, r, "upload_complete", "upload", r.PathValue("uploadId"), "{}") {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"targetPath": result.RelativePath, "size": result.TotalBytes})
}

func (s *Server) cancelUpload(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if err := s.memberFiles.CancelUpload(r.Context(), access.Subject{AccountID: session.AccountID}, r.PathValue("uploadId")); err != nil {
		// The legacy DELETE endpoint is idempotent for unknown/expired uploads.
		if !errors.Is(err, memberfiles.ErrUploadNotFound) && !errors.Is(err, memberfiles.ErrUploadExpired) {
			writeMemberFilesError(w, r, err, "upload requires current editor permission")
			return
		}
	}
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
	type indexJobListEntry struct {
		job         jobs.Job
		claimedAt   string
		claimedBy   string
		lastError   string
		completedAt string
		mountID     string
	}
	entries := []indexJobListEntry{}
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
		entries = append(entries, indexJobListEntry{
			job:         job,
			claimedAt:   claimedAt,
			claimedBy:   claimedBy,
			lastError:   lastError,
			completedAt: completedAt,
			mountID:     indexJobMountID(job.PayloadJSON),
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		writeDBError(w, r, err)
		return
	}
	if err := rows.Close(); err != nil {
		writeDBError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, entry := range entries {
		mount, err := s.mountAdmin.LoadMount(r.Context(), session.AccountID, entry.mountID)
		if errors.Is(err, mountadmin.ErrNotFound) {
			continue
		}
		if err != nil {
			writeDBError(w, r, err)
			return
		}
		items = append(items, jobResponse(entry.job, entry.claimedAt, entry.claimedBy, entry.lastError, entry.completedAt, mount.DisplayName))
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
	visibleMount, err := s.mountAdmin.LoadMount(r.Context(), session.AccountID, req.MountID)
	if errors.Is(err, mountadmin.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "mount_not_found", "mount was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
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
	if mount.Status != catalog.MountStatusActive || !mount.IndexEnabled || visibleMount.Status != "active" || !visibleMount.IndexEnabled {
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
	if !s.recordAuditMutation(w, r, "index_job_enqueue", "job", job.ID, "{}") {
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, jobResponse(job, "", "", "", "", visibleMount.DisplayName))
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
	visibleMount, err := s.mountAdmin.LoadMount(r.Context(), session.AccountID, payload.MountID)
	if errors.Is(err, mountadmin.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "job was not found")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	mount, err := s.loadCatalogMount(r, payload.MountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if mount.Status != catalog.MountStatusActive || !mount.IndexEnabled || visibleMount.Status != "active" || !visibleMount.IndexEnabled {
		httpx.WriteError(w, r, http.StatusConflict, "mount_not_indexable", "mount must be active with indexing enabled")
		return
	}
	if err := s.guard.VerifyMountIdentity(r.Context(), access.AuthorizedMount{ID: mount.ID, Root: mount.Root, MountRoot: mount.Root, IdentityJSON: mount.IdentityJSON}); err != nil {
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
	result, err := s.runCatalogScanBatch(r.Context(), store, *claimed)
	if err != nil {
		httpx.WriteError(w, r, http.StatusConflict, "index_failed", err.Error())
		return
	}
	if !s.recordAuditMutation(w, r, "index_job_run", "job", job.ID, "{}") {
		return
	}
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
SELECT ae.occurred_at,
       COALESCE(ae.actor_account_id, 'system'),
       COALESCE(actor.email, ''),
       COALESCE(actor.display_name, ''),
       COALESCE(ae.route_group, ''),
       ae.action,
       ae.target_type,
       COALESCE(ae.target_id, ''),
       COALESCE(
         CASE ae.target_type
           WHEN 'account' THEN COALESCE(NULLIF(target_account.display_name, ''), target_account.email)
           WHEN 'mount' THEN target_mount.display_name
           WHEN 'share' THEN COALESCE(NULLIF(target_share.relative_path, ''), target_share.public_id)
           WHEN 'ai_token' THEN COALESCE(NULLIF(target_token.name, ''), target_token.public_id)
           WHEN 'upload' THEN target_upload.target_relative_path
           WHEN 'job' THEN target_job.kind
           WHEN 'backup' THEN COALESCE(NULLIF(target_backup.path, ''), 'backup ' || target_backup.created_at)
           ELSE ae.target_id
         END,
         COALESCE(ae.target_id, '')
       ),
       ae.metadata_json
FROM audit_events ae
LEFT JOIN accounts actor ON actor.id = ae.actor_account_id
LEFT JOIN accounts target_account ON ae.target_type = 'account' AND target_account.id = ae.target_id
LEFT JOIN mounts target_mount ON ae.target_type = 'mount' AND target_mount.id = ae.target_id
LEFT JOIN shares target_share ON ae.target_type = 'share' AND target_share.id = ae.target_id
LEFT JOIN ai_tokens target_token ON ae.target_type = 'ai_token' AND target_token.id = ae.target_id
LEFT JOIN upload_sessions target_upload ON ae.target_type = 'upload' AND target_upload.id = ae.target_id
LEFT JOIN jobs target_job ON ae.target_type = 'job' AND target_job.id = ae.target_id
LEFT JOIN backups target_backup ON ae.target_type = 'backup' AND target_backup.id = ae.target_id
ORDER BY ae.id DESC
LIMIT ?
`, parseIntDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer rows.Close()
	items := []auditEventDTO{}
	for rows.Next() {
		var item auditEventDTO
		if err := rows.Scan(&item.OccurredAt, &item.Actor, &item.ActorEmail, &item.ActorDisplayName, &item.RouteGroup, &item.Action, &item.TargetType, &item.TargetID, &item.TargetLabel, &item.Metadata); err != nil {
			writeDBError(w, r, err)
			return
		}
		item.ActorLabel = auditActorLabel(item.Actor, item.ActorEmail, item.ActorDisplayName)
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// lookupMountEntry resolves metadata for a single mount-relative path by
// listing its parent directory, since files.Service does not expose a
// direct single-entry stat primitive.
func lookupMountEntry(mount files.Mount, relativePath string) (files.Entry, error) {
	if relativePath == "." {
		return files.Entry{Name: ".", RelativePath: ".", Kind: files.EntryKindDir}, nil
	}
	listing, err := files.NewService().ListDirectory(mount, path.Dir(relativePath))
	if err != nil {
		return files.Entry{}, err
	}
	base := path.Base(relativePath)
	for _, entry := range listing.Entries {
		if entry.Name == base {
			return entry, nil
		}
	}
	return files.Entry{}, os.ErrNotExist
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if !s.routeEnabled(domain.RouteGroupShare) {
		httpx.WriteError(w, r, http.StatusConflict, "share_route_disabled", "share route group is disabled; enable the Share route group to create share links")
		return
	}
	var req struct {
		Source          string `json:"source"`
		MountID         string `json:"mountId"`
		CollaborationID string `json:"collaborationId"`
		RelativePath    string `json:"relativePath"`
		RelativePathAlt string `json:"relative_path"`
		Password        string `json:"password"`
		AllowPreview    *bool  `json:"allowPreview"`
		AllowDownload   *bool  `json:"allowDownload"`
		MaxVisits       *int   `json:"maxVisits"`
		MaxVisitsAlt    *int   `json:"max_visits"`
		MaxDownloads    *int   `json:"maxDownloads"`
		MaxDownloadsAlt *int   `json:"max_downloads"`
		ExpiresAt       string `json:"expiresAt"`
		ExpiresAtAlt    string `json:"expires_at"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Source) == "" {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "source is required")
		return
	}
	if req.MaxVisits == nil {
		req.MaxVisits = req.MaxVisitsAlt
	}
	if req.MaxDownloads == nil {
		req.MaxDownloads = req.MaxDownloadsAlt
	}
	if req.ExpiresAt == "" {
		req.ExpiresAt = req.ExpiresAtAlt
	}
	var expiresAt time.Time
	if req.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "expiresAt must be RFC3339")
			return
		}
		expiresAt = parsed.UTC()
	}
	var maxVisits, maxDownloads *int64
	if req.MaxVisits != nil {
		value := int64(*req.MaxVisits)
		maxVisits = &value
	}
	if req.MaxDownloads != nil {
		value := int64(*req.MaxDownloads)
		maxDownloads = &value
	}
	locator := access.Locator{Source: contentref.Source(req.Source), MountID: req.MountID, Path: req.RelativePath}
	if req.Source == string(contentref.SourceCollaboration) {
		locator.CollaborationID = req.CollaborationID
	}
	issued, err := s.memberShares.CreateSecure(r.Context(), access.Subject{AccountID: session.AccountID}, membershare.CreateRequest{
		Locator:  locator,
		Password: req.Password, AllowPreview: req.AllowPreview, AllowDownload: req.AllowDownload,
		MaxVisits: maxVisits, MaxDownloads: maxDownloads, ExpiresAt: expiresAt,
	}, func(ctx context.Context, tx *sql.Tx, issued membershare.IssuedShare) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "share_create", "share", issued.ID, "{}")
	})
	if err != nil {
		writeMemberShareError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":        issued.ID,
		"publicId":  issued.PublicID,
		"secret":    issued.Secret,
		"fragment":  issued.Fragment,
		"expiresAt": issued.ExpiresAt,
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
       COALESCE(group_concat(b.source || char(31) || COALESCE(b.mount_id, '') || char(31) ||
           CASE WHEN b.source = 'common_mount' THEN COALESCE(m.display_name, '') ELSE '' END || char(31) ||
           b.relative_path, char(30)), '')
FROM ai_tokens t
LEFT JOIN ai_token_boundaries b ON b.token_id = t.id
LEFT JOIN mounts m ON b.source = 'common_mount' AND m.id = b.mount_id
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
			Source  contentref.Source `json:"source"`
			MountID string            `json:"mountId"`
			Path    string            `json:"path"`
		} `json:"boundaries"`
		ExpiresAt string `json:"expiresAt"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// An empty expiresAt creates a token that never expires; otherwise it must
	// be a future RFC3339 timestamp.
	var expiresAt time.Time
	if req.ExpiresAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, req.ExpiresAt)
		if parseErr != nil || !time.Now().UTC().Before(parsed.UTC()) {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "expiresAt must be a future RFC3339 timestamp")
			return
		}
		expiresAt = parsed
	}
	scopes := make([]aitoken.Scope, 0, len(req.Scopes))
	for _, scope := range req.Scopes {
		scopes = append(scopes, aitoken.Scope(scope))
	}
	boundaries := make([]aitoken.DirectoryBoundary, 0, len(req.Boundaries))
	for _, raw := range req.Boundaries {
		boundaries = append(boundaries, aitoken.DirectoryBoundary{
			Source: raw.Source, MountID: raw.MountID, RelativePath: raw.Path,
		})
	}
	if tokenScopesRequireWrite(scopes) {
		for _, boundary := range boundaries {
			if boundary.Source == aitoken.SourceAllAccountContent {
				httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "write scopes require explicit personal or common mount boundaries")
				return
			}
			if _, err := access.NewGuard(s.sqlDB()).Authorize(r.Context(), access.CheckRequest{
				Subject:            access.Subject{AccountID: session.AccountID},
				Scope:              aitoken.ScopeFilesWrite,
				Locator:            access.Locator{Source: boundary.Source, MountID: boundary.MountID, Path: boundary.RelativePath},
				RequiredPermission: domain.ContentPermissionEditor,
				Write:              true,
			}); err != nil {
				httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "write scopes require editor permission on a read-write content source")
				return
			}
		}
	}
	issued, err := s.tokens.CreateSecure(r.Context(), aitoken.CreateRequest{
		AccountID:  session.AccountID,
		Name:       req.Name,
		Scopes:     scopes,
		Boundaries: boundaries,
		ExpiresAt:  expiresAt,
	}, func(ctx context.Context, tx *sql.Tx, token aitoken.Token) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "ai_token_create", "ai_token", token.ID, "{}")
	})
	if err != nil {
		writeTokenError(w, r, err)
		return
	}
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
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `
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
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "ai_token_revoke", "ai_token", r.PathValue("tokenId"), "{}"); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
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
	if decision := s.checkCredentialRateLimit(r, ratelimit.ScopeShareExchange, req.PublicID); !decision.Allowed {
		writeRateLimited(w, r, decision)
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
			decision := s.recordCredentialFailure(r, ratelimit.ScopeShareExchange, req.PublicID)
			if !decision.Allowed {
				writeRateLimited(w, r, decision)
				return
			}
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
	s.recordCredentialSuccess(r, ratelimit.ScopeShareExchange, req.PublicID)
	if result.PasswordRequired {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "password_required"})
		return
	}
	if s.httpPolicy != nil {
		if _, err := SetCSRFCookie(w, s.cookieNames(r).ShareCSRF, isSecureRequest(r), time.Now().UTC()); err != nil {
			writeDBError(w, r, err)
			return
		}
	}
	http.SetCookie(w, s.shareSessionCookie(r, result.SessionToken, result.ExpiresAt))
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"status":         "created",
		"shareSessionId": result.SessionID,
		"expiresAt":      result.ExpiresAt,
	})
}

func (s *Server) routeGroups() []routeGroupDTO {
	items := make([]routeGroupDTO, 0, len(domain.AllRouteGroups))
	for _, group := range domain.AllRouteGroups {
		items = append(items, s.routeGroupDTO(group))
	}
	return items
}

func (s *Server) adminToggleableRouteGroups() []routeGroupDTO {
	items := make([]routeGroupDTO, 0, len(domain.AdminToggleableRouteGroups))
	for _, group := range domain.AdminToggleableRouteGroups {
		items = append(items, s.routeGroupDTO(group))
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
	session, err := s.requireAuthenticatedSession(r)
	if err != nil {
		return identity.Session{}, err
	}
	if session.Purpose != identity.SessionPurposeFull {
		return identity.Session{}, identity.ErrSessionInvalid
	}
	return session, nil
}

func (s *Server) requireAuthenticatedSession(r *http.Request) (identity.Session, error) {
	db := s.sqlDB()
	if db == nil {
		return identity.Session{}, identity.ErrSessionInvalid
	}
	cookie, err := r.Cookie(s.cookieNames(r).Session)
	if err != nil {
		return identity.Session{}, identity.ErrSessionInvalid
	}
	session, err := identity.New(db, identity.Options{}).VerifySession(r.Context(), cookie.Value)
	if err != nil {
		return identity.Session{}, err
	}
	return session, nil
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

var errMountNotWritable = errors.New("mount root is not writable at the container filesystem layer")

// isSlotPath reports whether candidatePath is a direct child of the
// predeclared external mount root (a slot). Unbound slots are suggestions only;
// an administrator registers the selected directory itself as the mount root.
func (s *Server) isSlotPath(candidatePath string) bool {
	root := filepath.Clean(strings.TrimSpace(s.cfg.Storage.PredeclaredMountRoot))
	candidatePath = filepath.Clean(strings.TrimSpace(candidatePath))
	if root == "." || root == string(filepath.Separator) || candidatePath == root {
		return false
	}
	return filepath.Dir(candidatePath) == root
}

func probeMountWritable(rootPath string) error {
	file, err := os.CreateTemp(rootPath, ".omnora-write-probe-*")
	if err != nil {
		return fmt.Errorf("%w: %w", errMountNotWritable, err)
	}
	name := file.Name()
	_ = file.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%w: %w", errMountNotWritable, err)
	}
	return nil
}

func isReadOnlyFilesystem(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "read-only file system") || strings.Contains(message, "erofs")
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

func (s *Server) loadCatalogMount(r *http.Request, mountID string) (catalog.Mount, error) {
	var mount catalog.Mount
	var indexEnabled int
	var identity string
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT id, root_path, status, index_enabled, COALESCE(mount_identity_json, '')
FROM mounts
WHERE id = ? AND status <> 'deleted'
`, mountID).Scan(&mount.ID, &mount.Root, &mount.Status, &indexEnabled, &identity)
	if err != nil {
		return catalog.Mount{}, err
	}
	mount.IndexEnabled = indexEnabled == 1
	mount.IdentityVerified = identity != ""
	mount.IdentityJSON = identity
	if mount.Status != catalog.MountStatusActive || !mount.IndexEnabled {
		return mount, nil
	}

	// The personal mount stores the stable sentinel "personal" in root_path;
	// resolve it through the identity-aware access loader before the catalog
	// scanner treats Root as a filesystem path. Common mounts are resolved by
	// the same loader so the worker and request paths share the same contract.
	loaded, err := access.NewGuard(s.sqlDB()).LoadMountIdentity(r.Context(), mountID)
	if err != nil {
		return catalog.Mount{}, err
	}
	mount.Source = loaded.Source
	mount.Root = loaded.MountRoot
	mount.IdentityVerified = strings.TrimSpace(loaded.IdentityJSON) != ""
	mount.IdentityJSON = loaded.IdentityJSON
	return mount, nil
}

func pathJoinForUpload(parentPath, fileName string) string {
	parentPath = strings.Trim(strings.TrimSpace(parentPath), "/")
	fileName = strings.Trim(strings.TrimSpace(fileName), "/")
	if parentPath == "" || parentPath == "." {
		return fileName
	}
	return parentPath + "/" + fileName
}

func tokenScopesRequireWrite(scopes []aitoken.Scope) bool {
	for _, scope := range scopes {
		switch scope {
		case aitoken.ScopeUploadsCreate,
			aitoken.ScopeFilesWrite,
			aitoken.ScopeFilesTrash,
			aitoken.ScopeFilesRestore,
			aitoken.ScopeFilesPurge,
			aitoken.ScopeSharesCreate:
			return true
		}
	}
	return false
}

func (s *Server) aiTokenBoundaries(r *http.Request, tokenID string) ([]map[string]string, error) {
	rows, err := s.sqlDB().QueryContext(r.Context(), `
SELECT b.source, COALESCE(b.mount_id, ''),
       CASE WHEN b.source = 'common_mount' THEN COALESCE(m.display_name, '') ELSE '' END,
       b.relative_path
FROM ai_token_boundaries b
LEFT JOIN mounts m ON b.source = 'common_mount' AND m.id = b.mount_id
WHERE b.token_id = ?
ORDER BY b.source, b.mount_id, b.relative_path
	`, tokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]string{}
	for rows.Next() {
		var source, mountID, mountName, relativePath string
		if err := rows.Scan(&source, &mountID, &mountName, &relativePath); err != nil {
			return nil, err
		}
		items = append(items, aiTokenBoundaryResponse(source, mountID, mountName, relativePath))
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
		if len(parts) == 4 {
			items = append(items, aiTokenBoundaryResponse(parts[0], parts[1], parts[2], parts[3]))
		}
	}
	return items
}

func aiTokenBoundaryResponse(source, mountID, mountName, relativePath string) map[string]string {
	item := map[string]string{"source": source}
	if source == string(contentref.SourceCommonMount) {
		item["mountId"] = mountID
		if mountName != "" {
			item["mountName"] = mountName
		}
	}
	if relativePath != "" {
		item["path"] = relativePath
	}
	return item
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

func jobResponse(job jobs.Job, claimedAt, claimedBy, lastError, completedAt, mountName string) map[string]any {
	return map[string]any{
		"id":          job.ID,
		"kind":        job.Kind,
		"priority":    job.Priority,
		"status":      job.Status,
		"mountName":   mountName,
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

func indexJobMountID(payloadJSON string) string {
	var payload struct {
		MountID string `json:"mount_id"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.MountID)
}

func parseIntDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// isSecureRequest reports whether the client connection is HTTPS.
func isSecureRequest(r *http.Request) bool {
	if secure, ok := r.Context().Value(secureRequestContext).(bool); ok {
		return secure
	}
	return r.TLS != nil
}

func (s *Server) sessionCookie(r *http.Request, token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     s.cookieNames(r).Session,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieNames(r).Session,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})
	ClearCSRFCookie(w, s.cookieNames(r).CSRF, isSecureRequest(r))
}

const shareSessionCookieName = "omnora_dev_share_session"

func (s *Server) shareSessionCookie(r *http.Request, token string, expiresAt time.Time) *http.Cookie {
	return &http.Cookie{
		Name:  s.cookieNames(r).ShareSession,
		Value: token,
		// Path "/" (rather than "/share/") so both the share portal SPA
		// (/share/...) and the share portal REST API (/api/v1/share/...)
		// receive this cookie on every request.
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	}
}

func accountResponse(account identity.Account) map[string]string {
	return map[string]string{
		"id": account.ID, "email": account.Email, "displayName": account.DisplayName, "role": string(account.Role), "status": account.Status,
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

func mountIdentityMatches(stored, current mountid.Identity) bool {
	return access.MountIdentityMatches(stored, current)
}

func mountIdentityNeedsRefresh(stored, current mountid.Identity) bool {
	return access.MountIdentityNeedsRefresh(stored, current)
}

func auditActorLabel(actorID, email, displayName string) string {
	if actorID == "" || actorID == "system" {
		return "system"
	}
	if strings.TrimSpace(displayName) != "" {
		return displayName
	}
	if strings.TrimSpace(email) != "" {
		return email
	}
	return actorID
}

func auditTargetLabel(targetType, targetID, targetLabel string) string {
	label := strings.TrimSpace(targetLabel)
	if label == "" {
		label = strings.TrimSpace(targetID)
	}
	return strings.TrimSpace(strings.TrimSpace(targetType) + " " + label)
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
		RequestID:      httpx.RequestID(r.Context()),
		IPHash:         audit.HashForAudit([]byte(s.cfg.Secrets.AuditHMACKey), audit.HashDomainClientIP, clientIPFromRequest(r)),
		UserAgentHash:  audit.HashForAudit([]byte(s.cfg.Secrets.AuditHMACKey), audit.HashDomainUserAgent, r.UserAgent()),
	}
	recorder := s.auditRecorder
	if err := recorder.Record(r.Context(), event); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		return audit.WrapWriteError(err)
	}
	_ = recorder.ClearReadinessRisk(r.Context())
	return nil
}

// recordAuditTx is the database-only mutation path. The caller must have
// completed authentication and authorization before opening tx; this helper
// performs no database reads and therefore cannot accidentally re-enter the
// same SQLite connection while the transaction is open.
func (s *Server) recordAuditTx(ctx context.Context, tx *sql.Tx, r *http.Request, actorID, action, targetType, targetID, metadata string) error {
	if tx == nil {
		return errors.New("audit transaction is nil")
	}
	if err := s.auditRecorder.RecordTx(ctx, tx, audit.Event{
		ActorAccountID: actorID,
		RouteGroup:     domain.RouteGroupREST,
		Action:         action,
		TargetType:     targetType,
		TargetID:       targetID,
		MetadataJSON:   metadata,
		Result:         "success",
		RequestID:      httpx.RequestID(r.Context()),
		IPHash:         audit.HashForAudit([]byte(s.cfg.Secrets.AuditHMACKey), audit.HashDomainClientIP, clientIPFromRequest(r)),
		UserAgentHash:  audit.HashForAudit([]byte(s.cfg.Secrets.AuditHMACKey), audit.HashDomainUserAgent, r.UserAgent()),
	}); err != nil {
		return audit.WrapWriteError(err)
	}
	return nil
}

func (s *Server) markAuditRiskIfNeeded(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, audit.ErrWriteFailed) {
		_ = s.auditRecorder.MarkReadinessRisk(ctx, err)
	}
}

func (s *Server) recordAuditMutation(w http.ResponseWriter, r *http.Request, action, targetType, targetID, metadata string) bool {
	if err := s.recordAudit(r, action, targetType, targetID, metadata); err != nil {
		writeDBError(w, r, err)
		return false
	}
	return true
}

func riskTone(ok bool) string {
	if ok {
		return "ok"
	}
	return "warn"
}
