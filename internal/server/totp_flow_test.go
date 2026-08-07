package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/store"
	"omnora/internal/totp"
)

func TestTOTPSetupPreservesActiveSecretAndConfirmRevokesOtherSessions(t *testing.T) {
	db, handler := newTOTPTestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	currentCookie := issueAPITestSession(t, db, admin.ID)
	otherCookie := issueAPITestSession(t, db, admin.ID)
	if _, err := db.SQL().ExecContext(context.Background(), `
UPDATE accounts
SET totp_required = 1, totp_secret_ciphertext = 'existing-active-ciphertext', totp_confirmed_at = ?
WHERE id = ?
`, time.Now().UTC().Format(time.RFC3339Nano), admin.ID); err != nil {
		t.Fatalf("seed active TOTP: %v", err)
	}

	setupReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/setup", bytes.NewReader([]byte(`{}`)))
	setupReq.Header.Set("Content-Type", "application/json")
	setupReq.AddCookie(currentCookie)
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("TOTP setup status = %d, body = %s", setupRec.Code, setupRec.Body.String())
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(setupRec.Body.Bytes(), &setup); err != nil {
		t.Fatalf("decode TOTP setup: %v", err)
	}
	if setup.Secret == "" {
		t.Fatal("TOTP setup did not return a secret")
	}
	var active, pending string
	if err := db.SQL().QueryRowContext(context.Background(), `
SELECT totp_secret_ciphertext, totp_pending_secret_ciphertext
FROM accounts WHERE id = ?
`, admin.ID).Scan(&active, &pending); err != nil {
		t.Fatalf("query TOTP setup state: %v", err)
	}
	if active != "existing-active-ciphertext" || pending == "" {
		t.Fatalf("setup changed active/pending state to %q/%q", active, pending)
	}

	code, err := totp.GenerateCode(setup.Secret, time.Now().UTC(), totp.Config{})
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	confirmBody, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatalf("marshal TOTP confirmation: %v", err)
	}
	confirmReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/confirm", bytes.NewReader(confirmBody))
	confirmReq.Header.Set("Content-Type", "application/json")
	confirmReq.AddCookie(currentCookie)
	confirmRec := httptest.NewRecorder()
	handler.ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("TOTP confirmation status = %d, body = %s", confirmRec.Code, confirmRec.Body.String())
	}

	var required int
	if err := db.SQL().QueryRowContext(context.Background(), `
SELECT totp_required FROM accounts WHERE id = ?
`, admin.ID).Scan(&required); err != nil {
		t.Fatalf("query TOTP required: %v", err)
	}
	if required != 1 {
		t.Fatalf("totp_required = %d, want 1", required)
	}
	if err := db.SQL().QueryRowContext(context.Background(), `
SELECT totp_secret_ciphertext, COALESCE(totp_pending_secret_ciphertext, '')
FROM accounts WHERE id = ?
`, admin.ID).Scan(&active, &pending); err != nil {
		t.Fatalf("query promoted TOTP state: %v", err)
	}
	if active == "existing-active-ciphertext" || pending != "" {
		t.Fatalf("promoted TOTP state = active %q pending %q", active, pending)
	}

	identityService := identity.New(db.SQL(), identity.Options{})
	if _, err := identityService.VerifySession(context.Background(), currentCookie.Value); err == nil {
		t.Fatal("old enrollment session remained valid after TOTP confirmation")
	}
	cookies := confirmRec.Result().Cookies()
	var rotatedCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == "omnora_dev_session" || cookie.Name == "__Host-omnora_session" {
			rotatedCookie = cookie
			break
		}
	}
	if rotatedCookie == nil || rotatedCookie.Value == "" {
		t.Fatalf("confirm response missing rotated session cookie: %#v", cookies)
	}
	current, err := identityService.VerifySession(context.Background(), rotatedCookie.Value)
	if err != nil {
		t.Fatalf("verify rotated session: %v", err)
	}
	if current.Purpose != identity.SessionPurposeFull {
		t.Fatalf("rotated session purpose = %q, want full", current.Purpose)
	}
	if _, err := identityService.VerifySession(context.Background(), otherCookie.Value); err == nil {
		t.Fatal("other session remained valid after TOTP confirmation")
	}
}

func newTOTPTestServer(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "totp-api-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open TOTP test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery control ready: %v", err)
	}
	return db, New(config.Config{
		Secrets: config.SecretConfig{TOTPEncryptionKey: "test-totp-encryption-key-0123456789"},
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
}
