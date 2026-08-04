package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/totp"
)

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	session, err := s.requireSession(r)
	if err != nil {
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthorized", "session is not valid")
		return
	}
	var email, displayName string
	var totpRequired int
	err = s.sqlDB().QueryRowContext(r.Context(), `
SELECT email, display_name, totp_required
FROM accounts
WHERE id = ? AND status = 'active'
`, session.AccountID).Scan(&email, &displayName, &totpRequired)
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
		"email":       email,
		"displayName": displayName,
		"totpEnabled": totpRequired == 1,
		"theme":       theme,
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
		RevokeTokens    bool   `json:"revokeTokens"`
		RevokeShares    bool   `json:"revokeShares"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := identity.New(s.sqlDB(), identity.Options{}).ChangePassword(r.Context(), session.AccountID, req.CurrentPassword, req.NewPassword); err != nil {
		s.writeIdentityError(w, r, err)
		return
	}
	if req.RevokeTokens {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, _ = s.sqlDB().ExecContext(r.Context(), `
UPDATE ai_tokens SET revoked_at = ?, updated_at = ? WHERE account_id = ? AND revoked_at IS NULL
`, now, now, session.AccountID)
	}
	if req.RevokeShares {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, _ = s.sqlDB().ExecContext(r.Context(), `
UPDATE shares SET revoked_at = ?, updated_at = ? WHERE creator_account_id = ? AND revoked_at IS NULL
`, now, now, session.AccountID)
	}
	_ = s.recordAudit(r, "account_password_change", "account", session.AccountID, "{}")
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
	if err := identity.New(s.sqlDB(), identity.Options{}).RevokeSessionByID(r.Context(), session.AccountID, sessionID); err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			httpx.WriteError(w, r, http.StatusNotFound, "not_found", "session was not found")
			return
		}
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "account_session_revoke", "session", sessionID, "{}")
	if sessionID == session.ID {
		clearSessionCookie(w, r)
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
			httpx.WriteError(w, r, http.StatusUnauthorized, "invalid_totp", "a valid TOTP code is required to disable TOTP")
			return
		}
	}
	if err := identity.New(s.sqlDB(), identity.Options{}).DisableTOTP(r.Context(), session.AccountID); err != nil {
		writeDBError(w, r, err)
		return
	}
	_ = s.recordAudit(r, "totp_disable", "account", session.AccountID, "{}")
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
