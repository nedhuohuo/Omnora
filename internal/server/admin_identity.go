package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"omnora/internal/httpx"
)

func (s *Server) initialAdminID(ctx context.Context) (string, error) {
	var accountID string
	err := s.sqlDB().QueryRowContext(ctx, `SELECT value FROM system_state WHERE key = 'initial_admin_account_id'`).Scan(&accountID)
	if err == nil && strings.TrimSpace(accountID) != "" {
		return accountID, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = s.sqlDB().QueryRowContext(ctx, `SELECT id FROM accounts ORDER BY created_at ASC, id ASC LIMIT 1`).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return accountID, err
}

func (s *Server) rejectInitialAdminMutation(w http.ResponseWriter, r *http.Request, accountID, message string) bool {
	initialAdminID, err := s.initialAdminID(r.Context())
	if err != nil {
		writeDBError(w, r, err)
		return true
	}
	if initialAdminID == "" || initialAdminID != accountID {
		return false
	}
	httpx.WriteError(w, r, http.StatusConflict, "initial_admin_protected", message)
	return true
}
