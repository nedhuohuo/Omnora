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
)

const trustBoundaryTestOrigin = "https://files.example.test"

// newTrustBoundaryAPITestServer builds a full server with a real HTTP trust
// policy and HTTPS public origin, so routeSecurity runs the production CSRF and
// recent-reauthentication checks against production cookie names instead of the
// in-process short circuit used by the other API test helpers.
func newTrustBoundaryAPITestServer(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "trust-boundary-api-test.db"),
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
	srv := NewServer(config.Config{
		Storage:           config.StorageConfig{ManagedDir: managedDir},
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		HTTP:              config.HTTPConfig{PublicURL: trustBoundaryTestOrigin},
	}, db)
	srv.RequireHTTPTrustBoundary()
	return db, srv.Handler()
}

// trustBoundaryRequest builds an HTTPS request that satisfies the real trust
// boundary: the allowed Host, an allowed Origin for unsafe methods, and a stable
// peer address. It attaches the production session cookie and, when csrfToken is
// non-empty, the matching double-submit CSRF pair.
func trustBoundaryRequest(t *testing.T, method, path string, body []byte, sessionToken, csrfToken string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, trustBoundaryTestOrigin+path, bytes.NewReader(body))
	req.Host = "files.example.test"
	req.RemoteAddr = "198.51.100.10:5678"
	if unsafeHTTPMethod(method) {
		req.Header.Set("Origin", trustBoundaryTestOrigin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: productionCookieNames.Session, Value: sessionToken, Path: "/"})
	}
	if csrfToken != "" {
		req.AddCookie(&http.Cookie{Name: productionCookieNames.CSRF, Value: csrfToken, Path: "/"})
		req.Header.Set(CSRFHeaderName, csrfToken)
	}
	return req
}

// serveTrustBoundaryRequest serves the request and returns the recorder plus any
// rotated production session and CSRF cookies from the response.
func serveTrustBoundaryRequest(handler http.Handler, req *http.Request) (*httptest.ResponseRecorder, string, string) {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var sessionToken, csrfToken string
	for _, cookie := range rec.Result().Cookies() {
		switch cookie.Name {
		case productionCookieNames.Session:
			sessionToken = cookie.Value
		case productionCookieNames.CSRF:
			csrfToken = cookie.Value
		}
	}
	return rec, sessionToken, csrfToken
}

func TestAdminUserCreateRequiresRecentReauth(t *testing.T) {
	db, handler := newTrustBoundaryAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminToken := issueAPITestSession(t, db, admin.ID).Value
	memberToken := issueAPITestSession(t, db, member.ID).Value
	svc := identity.New(db.SQL(), identity.Options{})

	// A fresh full session has no reauthentication marker. Obtain the browser
	// CSRF pair with a safe request before any mutation.
	rec, _, csrf := serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodGet, "/api/v1/bootstrap", nil, adminToken, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if csrf == "" {
		t.Fatalf("bootstrap did not issue a CSRF cookie: %#v", rec.Result().Cookies())
	}

	createBody, err := json.Marshal(map[string]any{
		"email":       "newadmin@example.test",
		"displayName": "New Admin",
		"password":    "NewCorrectHorse3!",
		"role":        "admin",
	})
	if err != nil {
		t.Fatalf("marshal create admin user: %v", err)
	}

	// Without recent reauthentication the mutation is rejected in routeSecurity
	// before the handler runs, so neither an account nor an audit row appears.
	rec, _, _ = serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, "/api/v1/admin/users", createBody, adminToken, csrf))
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"reauthentication_required"`)) {
		t.Fatalf("stale admin create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM accounts WHERE email = 'newadmin@example.test'`).Scan(&count); err != nil {
		t.Fatalf("count created account: %v", err)
	}
	if count != 0 {
		t.Fatalf("reauth-gated create still created an account (count = %d)", count)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM audit_events WHERE action = 'admin_user_create'`).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("reauth-gated create still wrote audit rows (count = %d)", count)
	}

	// Reauthenticate with the correct password. The response rotates the session
	// (stamping a recent-reauth marker) and issues a fresh CSRF pair.
	reauthBody, err := json.Marshal(map[string]string{"password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal reauthenticate: %v", err)
	}
	rec, rotatedToken, rotatedCSRF := serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, "/api/v1/account/reauthenticate", reauthBody, adminToken, csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("reauthenticate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rotatedToken == "" || rotatedCSRF == "" {
		t.Fatalf("reauthenticate did not rotate session/CSRF cookies: %#v", rec.Result().Cookies())
	}

	// The retried mutation now succeeds and the new account authenticates.
	rec, _, _ = serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, "/api/v1/admin/users", createBody, rotatedToken, rotatedCSRF))
	if rec.Code != http.StatusCreated {
		t.Fatalf("reauth retry create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := svc.Authenticate(context.Background(), "newadmin@example.test", "NewCorrectHorse3!"); err != nil {
		t.Fatalf("new admin cannot authenticate: %v", err)
	}

	// A non-admin still cannot create users even after a successful
	// reauthentication; requireAdmin rejects it after the reauth gate passes.
	rec, _, memberCSRF := serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodGet, "/api/v1/bootstrap", nil, memberToken, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("member bootstrap status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec, memberRotated, memberRotatedCSRF := serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, "/api/v1/account/reauthenticate", reauthBody, memberToken, memberCSRF))
	if rec.Code != http.StatusOK {
		t.Fatalf("member reauthenticate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec, _, _ = serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, "/api/v1/admin/users", createBody, memberRotated, memberRotatedCSRF))
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"forbidden"`)) {
		t.Fatalf("member create status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminUserEnableRequiresRecentReauth(t *testing.T) {
	db, handler := newTrustBoundaryAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	svc := identity.New(db.SQL(), identity.Options{})
	if err := svc.DisableAccountSecure(context.Background(), member.ID, nil); err != nil {
		t.Fatalf("disable target account: %v", err)
	}
	adminToken := issueAPITestSession(t, db, admin.ID).Value

	// A freshly issued full session is not a recent reauthentication. The
	// enable mutation must stop at routeSecurity before changing the target.
	rec, _, csrf := serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodGet, "/api/v1/bootstrap", nil, adminToken, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if csrf == "" {
		t.Fatalf("bootstrap did not issue a CSRF cookie: %#v", rec.Result().Cookies())
	}

	enablePath := "/api/v1/admin/users/" + member.ID + "/enable"
	rec, _, _ = serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, enablePath, nil, adminToken, csrf))
	if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"reauthentication_required"`)) {
		t.Fatalf("stale admin enable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var status string
	if err := db.SQL().QueryRow(`SELECT status FROM accounts WHERE id = ?`, member.ID).Scan(&status); err != nil {
		t.Fatalf("query target account status after rejected enable: %v", err)
	}
	if status != "disabled" {
		t.Fatalf("rejected enable changed target status to %q, want disabled", status)
	}

	// Reauthentication rotates the session with a recent-reauth marker. The
	// exact same enable mutation can then proceed.
	reauthBody, err := json.Marshal(map[string]string{"password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal reauthenticate: %v", err)
	}
	rec, rotatedToken, rotatedCSRF := serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, "/api/v1/account/reauthenticate", reauthBody, adminToken, csrf))
	if rec.Code != http.StatusOK {
		t.Fatalf("reauthenticate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rotatedToken == "" || rotatedCSRF == "" {
		t.Fatalf("reauthenticate did not rotate session/CSRF cookies: %#v", rec.Result().Cookies())
	}

	rec, _, _ = serveTrustBoundaryRequest(handler, trustBoundaryRequest(t, http.MethodPost, enablePath, nil, rotatedToken, rotatedCSRF))
	if rec.Code != http.StatusOK {
		t.Fatalf("reauthenticated admin enable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := db.SQL().QueryRow(`SELECT status FROM accounts WHERE id = ?`, member.ID).Scan(&status); err != nil {
		t.Fatalf("query target account status after accepted enable: %v", err)
	}
	if status != "active" {
		t.Fatalf("accepted enable status = %q, want active", status)
	}
}
