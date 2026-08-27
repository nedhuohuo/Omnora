package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"omnora/internal/audit"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/ratelimit"
	"omnora/internal/totp"
)

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var email, displayName string
	var totpRequired, passwordResetRequired int
	err = s.sqlDB().QueryRowContext(r.Context(), `
SELECT email, display_name, totp_required, password_reset_required
FROM accounts
WHERE id = ? AND status = 'active'
`, session.AccountID).Scan(&email, &displayName, &totpRequired, &passwordResetRequired)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	theme, err := s.loadAccountTheme(r, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"email":                    email,
		"displayName":              displayName,
		"totpEnabled":              totpRequired == 1,
		"passwordResetRecommended": passwordResetRequired == 1,
		"theme":                    theme,
	})
}

func (s *Server) changeAccountPassword(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		TOTPCode        string `json:"totpCode"`
		RevokeTokens    bool   `json:"revokeTokens"`
		RevokeShares    bool   `json:"revokeShares"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if decision := s.checkCredentialRateLimit(r, ratelimit.ScopeTOTP, session.AccountID); !decision.Allowed {
		writeRateLimited(w, r, decision)
		return
	}
	svc := identity.New(s.sqlDB(), identity.Options{})
	material, err := svc.LoadCredentialMaterial(r.Context(), session.AccountID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
		return
	}
	if !svc.VerifyPassword(req.CurrentPassword, material.PasswordHash) {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeTOTP, session.AccountID)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
		return
	}
	if material.TOTPRequired {
		secret, decryptErr := s.decryptTOTPSecret(material.TOTPSecretCiphertext)
		if decryptErr != nil || secret == "" || !totp.Verify(secret, strings.TrimSpace(req.TOTPCode), time.Now().UTC()) {
			decision := s.recordCredentialFailure(r, ratelimit.ScopeTOTP, session.AccountID)
			if !decision.Allowed {
				writeRateLimited(w, r, decision)
				return
			}
			httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_credentials", "credentials are not valid")
			return
		}
	}
	newHash, err := svc.HashPassword(req.NewPassword)
	if err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	recorder := s.auditRecorder
	rotated, err := svc.ChangePasswordSecure(r.Context(), identity.ChangePasswordSecureRequest{
		Session:         session,
		ExpectedOldHash: material.PasswordHash,
		NewPasswordHash: newHash,
		RevokeTokens:    req.RevokeTokens,
		RevokeShares:    req.RevokeShares,
	}, func(ctx context.Context, tx *sql.Tx) error {
		if err := recorder.RecordTx(ctx, tx, audit.Event{
			ActorAccountID: session.AccountID,
			RouteGroup:     domain.RouteGroupREST,
			Action:         "account_password_change",
			TargetType:     "account",
			TargetID:       session.AccountID,
			MetadataJSON:   "{}",
			IPHash:         audit.HashForAudit([]byte(s.cfg.Secrets.AuditHMACKey), audit.HashDomainClientIP, clientIPFromRequest(r)),
			UserAgentHash:  audit.HashForAudit([]byte(s.cfg.Secrets.AuditHMACKey), audit.HashDomainUserAgent, r.UserAgent()),
		}); err != nil {
			return audit.WrapWriteError(err)
		}
		return nil
	})
	if err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		s.writeIdentityError(w, r, err)
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

type sessionSummaryDTO struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"createdAt"`
	ExpiresAt  string `json:"expiresAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	Current    bool   `json:"current"`
}

func (s *Server) listAccountSessions(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	sessions, err := identity.New(s.sqlDB(), identity.Options{}).ListSessions(r.Context(), session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	items := make([]sessionSummaryDTO, 0, len(sessions))
	for _, entry := range sessions {
		item := sessionSummaryDTO{
			ID:        entry.ID,
			CreatedAt: entry.CreatedAt.Format(time.RFC3339Nano),
			ExpiresAt: entry.ExpiresAt.Format(time.RFC3339Nano),
			Current:   entry.ID == session.ID,
		}
		if !entry.LastUsedAt.IsZero() {
			item.LastUsedAt = entry.LastUsedAt.Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revokeAccountSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	sessionID := r.PathValue("sessionId")
	tx, err := s.sqlDB().BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `
UPDATE identity_sessions
SET revoked_at = ?
WHERE id = ? AND account_id = ? AND revoked_at IS NULL
`, time.Now().UTC().Format(time.RFC3339Nano), sessionID, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if affected != 1 {
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "session was not found")
		return
	}
	if err := s.recordAuditTx(r.Context(), tx, r, session.AccountID, "account_session_revoke", "session", sessionID, "{}"); err != nil {
		s.markAuditRiskIfNeeded(r.Context(), err)
		writeDBError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeDBError(w, r, err)
		return
	}
	if sessionID == session.ID {
		s.clearSessionCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) disableAccountTOTP(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var passwordHash string
	err = s.sqlDB().QueryRowContext(r.Context(), `
SELECT password_hash FROM accounts WHERE id = ? AND status = 'active'
`, session.AccountID).Scan(&passwordHash)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if !identity.New(s.sqlDB(), identity.Options{}).VerifyPassword(req.Password, passwordHash) {
		decision := s.recordCredentialFailure(r, ratelimit.ScopeTOTP, session.AccountID)
		if !decision.Allowed {
			writeRateLimited(w, r, decision)
			return
		}
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "password is not valid")
		return
	}
	account, err := s.loadAccountForTOTP(r, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	if account.Required {
		if strings.TrimSpace(req.Code) == "" || account.TOTPSecret == "" || !totp.Verify(account.TOTPSecret, req.Code, time.Now().UTC()) {
			decision := s.recordCredentialFailure(r, ratelimit.ScopeTOTP, session.AccountID)
			if !decision.Allowed {
				writeRateLimited(w, r, decision)
				return
			}
			httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_totp", "a valid TOTP code is required to disable TOTP")
			return
		}
	}
	if err := identity.New(s.sqlDB(), identity.Options{}).DisableTOTPSecure(r.Context(), session.AccountID, func(ctx context.Context, tx *sql.Tx) error {
		return s.recordAuditTx(ctx, tx, r, session.AccountID, "totp_disable", "account", session.AccountID, "{}")
	}); err != nil {
		writeDBError(w, r, err)
		return
	}
	s.recordCredentialSuccess(r, ratelimit.ScopeTOTP, session.AccountID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "disabled"})
}

func (s *Server) getAccountPreferences(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	theme, err := s.loadAccountTheme(r, session.AccountID)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"theme": theme})
}

func (s *Server) putAccountPreferences(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var req struct {
		Theme string `json:"theme"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Theme {
	case "system", "light", "dark":
	default:
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", "theme must be system, light, or dark")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.sqlDB().ExecContext(r.Context(), `
INSERT INTO account_preferences(account_id, theme, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET theme = excluded.theme, updated_at = excluded.updated_at
`, session.AccountID, req.Theme, now)
	if err != nil {
		writeDBError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"theme": req.Theme})
}

func (s *Server) loadAccountTheme(r *http.Request, accountID string) (string, error) {
	var theme string
	err := s.sqlDB().QueryRowContext(r.Context(), `
SELECT theme FROM account_preferences WHERE account_id = ?
`, accountID).Scan(&theme)
	if errors.Is(err, sql.ErrNoRows) {
		return "system", nil
	}
	if err != nil {
		return "", err
	}
	return theme, nil
}
