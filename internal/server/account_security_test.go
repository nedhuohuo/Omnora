package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/ratelimit"
	"omnora/internal/store"
	"omnora/internal/totp"
)

func TestChangeAccountPassword(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)

	const newPassword = "NewCorrectHorse2!"

	wrongBody, err := json.Marshal(map[string]any{
		"currentPassword": "definitely-wrong",
		"newPassword":     newPassword,
	})
	if err != nil {
		t.Fatalf("marshal wrong password request: %v", err)
	}
	wrongReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(wrongBody))
	wrongReq.Header.Set("Content-Type", "application/json")
	wrongReq.AddCookie(adminCookie)
	wrongRec := httptest.NewRecorder()
	handler.ServeHTTP(wrongRec, wrongReq)
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("change password with wrong current password status = %d, want %d, body = %s", wrongRec.Code, http.StatusUnauthorized, wrongRec.Body.String())
	}

	correctBody, err := json.Marshal(map[string]any{
		"currentPassword": apiTestPassword,
		"newPassword":     newPassword,
	})
	if err != nil {
		t.Fatalf("marshal correct password request: %v", err)
	}
	correctReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(correctBody))
	correctReq.Header.Set("Content-Type", "application/json")
	correctReq.AddCookie(adminCookie)
	correctRec := httptest.NewRecorder()
	handler.ServeHTTP(correctRec, correctReq)
	if correctRec.Code != http.StatusOK {
		t.Fatalf("change password status = %d, body = %s", correctRec.Code, correctRec.Body.String())
	}

	svc := identity.New(db.SQL(), identity.Options{})
	if _, err := svc.Authenticate(context.Background(), admin.Email, apiTestPassword); err == nil {
		t.Fatalf("expected old password to no longer authenticate")
	}
	if _, err := svc.Authenticate(context.Background(), admin.Email, newPassword); err != nil {
		t.Fatalf("expected new password to authenticate, got error: %v", err)
	}
}

func TestAdminCanDisableOwnTOTP(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	reqBody, err := json.Marshal(map[string]string{"password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal disable TOTP request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/disable", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, admin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin disable TOTP status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestChangeAccountPasswordRejectsInvalidTOTPInSameRequest(t *testing.T) {
	db, handler := newTOTPTestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, admin.ID)

	setupReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/totp/setup", bytes.NewReader([]byte(`{}`)))
	setupReq.Header.Set("Content-Type", "application/json")
	setupReq.AddCookie(cookie)
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
	confirmReq.AddCookie(cookie)
	confirmRec := httptest.NewRecorder()
	handler.ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("TOTP confirmation status = %d, body = %s", confirmRec.Code, confirmRec.Body.String())
	}
	var rotatedCookie *http.Cookie
	for _, responseCookie := range confirmRec.Result().Cookies() {
		if responseCookie.Name == sessionCookieName {
			rotatedCookie = responseCookie
			break
		}
	}
	if rotatedCookie == nil {
		t.Fatalf("TOTP confirmation did not rotate the session: %#v", confirmRec.Result().Cookies())
	}

	body, err := json.Marshal(map[string]string{
		"currentPassword": apiTestPassword,
		"newPassword":     "NewCorrectHorse2!",
		"totpCode":        "invalid",
	})
	if err != nil {
		t.Fatalf("marshal password change: %v", err)
	}
	changeReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(body))
	changeReq.Header.Set("Content-Type", "application/json")
	changeReq.AddCookie(rotatedCookie)
	changeRec := httptest.NewRecorder()
	handler.ServeHTTP(changeRec, changeReq)
	if changeRec.Code != http.StatusUnauthorized {
		t.Fatalf("password change with invalid TOTP status = %d, want %d, body = %s", changeRec.Code, http.StatusUnauthorized, changeRec.Body.String())
	}
}

// newRateLimitTestServer builds an API test server whose credential limiter
// reads a caller-controlled clock, so cooldown expiry can be advanced
// deterministically without sleeping. The Server is returned so tests can seed
// or inspect limiter state with the exact keys the handlers derive.
func newRateLimitTestServer(t *testing.T, now *time.Time) (*store.DB, *Server, http.Handler) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "rate-limit-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery control ready: %v", err)
	}
	managedDir := apiTestManagedDir(t, db)
	// The credential limiter keys are derived with audit.HashForAudit, which
	// deliberately returns no key for an empty HMAC secret. Provide a stable
	// non-empty secret so attempts actually accumulate on real keys.
	srv := NewServer(config.Config{
		Storage: config.StorageConfig{ManagedDir: managedDir},
		Secrets: config.SecretConfig{
			AuditHMACKey: "rate-limit-test-audit-hmac-key-0123456789abcdef0123456789",
		},
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db, func(s *Server) {
		s.authLimiter = ratelimit.New(ratelimit.Options{Clock: func() time.Time { return *now }})
	})
	return db, srv, srv.Handler()
}

// changeAccountPasswordRequest issues one PATCH /api/v1/account/password call
// with the given current/next password and source address.
func changeAccountPasswordRequest(handler http.Handler, cookie *http.Cookie, current, next, remoteAddr string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{
		"currentPassword": current,
		"newPassword":     next,
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestChangeAccountPasswordWrongOldPasswordRateLimited(t *testing.T) {
	now := time.Now().UTC()
	db, _, handler := newRateLimitTestServer(t, &now)
	admin, _ := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, admin.ID)

	const newPassword = "NewCorrectHorse2!"

	// The subject bucket cooldown activates at 5 failures; the first four
	// wrong-old-password attempts must be generic 401s.
	for i := 0; i < 4; i++ {
		rec := changeAccountPasswordRequest(handler, cookie, "definitely-wrong", newPassword, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d status = %d, want 401, body = %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := changeAccountPasswordRequest(handler, cookie, "definitely-wrong", newPassword, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("5th wrong attempt status = %d, want 429, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("5th wrong attempt must set Retry-After, got headers %#v", rec.Header())
	}

	// A correct old password during the subject cooldown is still blocked by
	// the pre-check and must not change the password.
	rec = changeAccountPasswordRequest(handler, cookie, apiTestPassword, newPassword, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password during cooldown status = %d, want 429, body = %s", rec.Code, rec.Body.String())
	}
	svc := identity.New(db.SQL(), identity.Options{})
	if _, err := svc.Authenticate(context.Background(), admin.Email, newPassword); err == nil {
		t.Fatalf("new password must not authenticate while the cooldown blocks the change")
	}

	// After the subject cooldown (1 minute) expires, the correct password
	// succeeds and the old password no longer works.
	now = now.Add(61 * time.Second)
	rec = changeAccountPasswordRequest(handler, cookie, apiTestPassword, newPassword, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("correct password after cooldown status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := svc.Authenticate(context.Background(), admin.Email, apiTestPassword); err == nil {
		t.Fatalf("old password must no longer authenticate after the change")
	}
	if _, err := svc.Authenticate(context.Background(), admin.Email, newPassword); err != nil {
		t.Fatalf("expected new password to authenticate after the change: %v", err)
	}
}

func TestChangeAccountPasswordSuccessClearsSubjectNotIP(t *testing.T) {
	now := time.Now().UTC()
	db, srv, handler := newRateLimitTestServer(t, &now)
	admin, _ := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, admin.ID)

	const newPassword = "NewCorrectHorse2!"
	const ipA = "203.0.113.10:5678"

	// Derive the same opaque keys the handlers read, so pressure seeded through
	// the limiter lands on the exact buckets the HTTP flow checks.
	seedReq := httptest.NewRequest(http.MethodGet, "/", nil)
	seedReq.RemoteAddr = ipA
	ipKey := srv.rateLimitKeys(seedReq, "").IP
	adminSubject := srv.rateLimitKeys(seedReq, admin.ID).Subject

	// Pressurize the admin subject bucket (5 failures, 1-minute cooldown) and
	// the shared client IP bucket (20 failures, 1-minute cooldown) without
	// letting any single subject escape the subject threshold.
	for i := 0; i < 5; i++ {
		srv.authLimiter.Failure(ratelimit.ScopeTOTP, ratelimit.Keys{IP: ipKey, Subject: adminSubject})
	}
	for i := 0; i < 15; i++ {
		srv.authLimiter.Failure(ratelimit.ScopeTOTP, ratelimit.Keys{IP: ipKey, Subject: fmt.Sprintf("seed-%d", i)})
	}

	// Let every cooldown expire while the failure counts remain stored.
	now = now.Add(61 * time.Second)

	// A correct password from a fresh IP succeeds and rotates the session. The
	// success must clear the subject bucket while retaining the IP pressure.
	rec := changeAccountPasswordRequest(handler, cookie, apiTestPassword, newPassword, "198.51.100.20:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("correct change status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var rotated *http.Cookie
	for _, responseCookie := range rec.Result().Cookies() {
		if responseCookie.Name == sessionCookieName {
			rotated = responseCookie
			break
		}
	}
	if rotated == nil {
		t.Fatalf("password change did not rotate the session: %#v", rec.Result().Cookies())
	}

	// Subject bucket cleared: a wrong attempt from a brand-new IP returns a
	// generic 401, not 429. If the success had left the subject bucket
	// pressurized, this attempt would have pushed it to 6 failures and hit its
	// cooldown.
	if rec := changeAccountPasswordRequest(handler, rotated, "definitely-wrong", newPassword, "192.0.2.5:4321"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong attempt from fresh IP after success status = %d, want 401 (subject bucket should be cleared), body = %s", rec.Code, rec.Body.String())
	}

	// IP bucket pressure retained: a wrong attempt from the seeded IP pushes it
	// to 21 failures, which reactivates the 1-minute cooldown. If the success
	// had cleared the IP bucket too, this attempt would have been a 401.
	if rec := changeAccountPasswordRequest(handler, rotated, "definitely-wrong", newPassword, ipA); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong attempt from seeded IP after success status = %d, want 429 (IP pressure retained), body = %s", rec.Code, rec.Body.String())
	}
}
