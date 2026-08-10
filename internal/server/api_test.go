package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/store"
)

func TestSessionResponsesIncludeCurrentAdminStatus(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)

	for _, tc := range []struct {
		name             string
		email            string
		wantAdmin        bool
		wantInitialAdmin bool
		wantID           string
	}{
		{name: "admin", email: admin.Email, wantAdmin: true, wantInitialAdmin: true, wantID: admin.ID},
		{name: "member", email: member.Email, wantAdmin: false, wantInitialAdmin: false, wantID: member.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"login": tc.email, "password": apiTestPassword})
			if err != nil {
				t.Fatalf("marshal login request: %v", err)
			}
			login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewReader(body))
			login.Header.Set("Content-Type", "application/json")
			loginRec := httptest.NewRecorder()
			handler.ServeHTTP(loginRec, login)
			if loginRec.Code != http.StatusCreated {
				t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
			}

			var loginResponse sessionResponse
			if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResponse); err != nil {
				t.Fatalf("decode login response: %v", err)
			}
			if loginResponse.UserID != tc.wantID || loginResponse.IsAdmin != tc.wantAdmin || loginResponse.IsInitialAdmin != tc.wantInitialAdmin || loginResponse.ExpiresAt.IsZero() {
				t.Fatalf("login response = %#v", loginResponse)
			}
			cookies := loginRec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("login cookies = %#v, want session cookie", cookies)
			}

			current := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
			current.AddCookie(cookies[0])
			currentRec := httptest.NewRecorder()
			handler.ServeHTTP(currentRec, current)
			if currentRec.Code != http.StatusOK {
				t.Fatalf("current session status = %d, body = %s", currentRec.Code, currentRec.Body.String())
			}
			var currentResponse sessionResponse
			if err := json.Unmarshal(currentRec.Body.Bytes(), &currentResponse); err != nil {
				t.Fatalf("decode current session response: %v", err)
			}
			if currentResponse.UserID != tc.wantID || currentResponse.IsAdmin != tc.wantAdmin || currentResponse.IsInitialAdmin != tc.wantInitialAdmin || currentResponse.ExpiresAt.IsZero() {
				t.Fatalf("current session response = %#v", currentResponse)
			}
		})
	}
}

func TestAdminLoginWithoutTOTPCreatesFullSession(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	body, err := json.Marshal(map[string]string{"login": admin.Email, "password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal login: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if response.Purpose != identity.SessionPurposeFull || response.RequiresTOTPEnrollment || !response.IsAdmin {
		t.Fatalf("login response = %#v, want full admin session without enrollment", response)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %#v", cookies)
	}
	session, err := identity.New(db.SQL(), identity.Options{}).VerifySession(context.Background(), cookies[0].Value)
	if err != nil {
		t.Fatalf("verify session: %v", err)
	}
	if session.Purpose != identity.SessionPurposeFull {
		t.Fatalf("session purpose = %q, want full", session.Purpose)
	}
}

func TestPasswordResetRequiredIsRecommendedAtLogin(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`UPDATE accounts SET password_reset_required = 1 WHERE id = ?`, member.ID); err != nil {
		t.Fatalf("mark password reset: %v", err)
	}

	// A reset-flagged account logs in with its current password alone; no new
	// password is demanded at login.
	body, err := json.Marshal(map[string]string{"login": member.Email, "password": apiTestPassword})
	if err != nil {
		t.Fatalf("marshal login: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var session struct {
		PasswordResetRecommended bool `json:"passwordResetRecommended"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if !session.PasswordResetRecommended {
		t.Fatalf("passwordResetRecommended = false, want true")
	}

	// Logging in must not clear the reset flag; it only recommends a change.
	var resetRequired int
	if err := db.SQL().QueryRow(`SELECT password_reset_required FROM accounts WHERE id = ?`, member.ID).Scan(&resetRequired); err != nil {
		t.Fatalf("query reset flag: %v", err)
	}
	if resetRequired != 1 {
		t.Fatalf("password_reset_required = %d, want 1 after login", resetRequired)
	}

	// Changing the password in the account area clears the recommendation.
	changeBody, err := json.Marshal(map[string]any{
		"currentPassword": apiTestPassword,
		"newPassword":     "NewCorrectHorse2!",
	})
	if err != nil {
		t.Fatalf("marshal change password request: %v", err)
	}
	changeReq := httptest.NewRequest(http.MethodPatch, "/api/v1/account/password", bytes.NewReader(changeBody))
	changeReq.Header.Set("Content-Type", "application/json")
	changeReq.AddCookie(issueAPITestSession(t, db, member.ID))
	changeRec := httptest.NewRecorder()
	handler.ServeHTTP(changeRec, changeReq)
	if changeRec.Code != http.StatusOK {
		t.Fatalf("change password status = %d, want %d, body = %s", changeRec.Code, http.StatusOK, changeRec.Body.String())
	}
	if err := db.SQL().QueryRow(`SELECT password_reset_required FROM accounts WHERE id = ?`, member.ID).Scan(&resetRequired); err != nil {
		t.Fatalf("query reset flag: %v", err)
	}
	if resetRequired != 0 {
		t.Fatalf("password_reset_required = %d, want 0 after change", resetRequired)
	}
}

func TestBootstrapReportsInitializationAvailability(t *testing.T) {
	db, handler := newAPITestServer(t)
	ctx := context.Background()
	service := identity.New(db.SQL(), identity.Options{})
	secret, err := service.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("prepare initialization: %v", err)
	}

	assertInitializationAvailable := func(want bool) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("bootstrap status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body struct {
			InitializationAvailable bool `json:"initializationAvailable"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode bootstrap: %v", err)
		}
		if body.InitializationAvailable != want {
			t.Fatalf("initializationAvailable = %v, want %v", body.InitializationAvailable, want)
		}
	}

	assertInitializationAvailable(true)
	initBody, err := json.Marshal(map[string]string{
		"token":       secret.Token,
		"email":       "admin@example.test",
		"displayName": "Admin",
		"password":    apiTestPassword,
	})
	if err != nil {
		t.Fatalf("marshal initialization request: %v", err)
	}
	initReq := httptest.NewRequest(http.MethodPost, "/api/v1/initialize", bytes.NewReader(initBody))
	initReq.Header.Set("Content-Type", "application/json")
	initRec := httptest.NewRecorder()
	handler.ServeHTTP(initRec, initReq)
	if initRec.Code != http.StatusCreated {
		t.Fatalf("initialize admin status = %d, body = %s", initRec.Code, initRec.Body.String())
	}

	var initialAdminID string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT value FROM system_state WHERE key = 'initial_admin_account_id'
`).Scan(&initialAdminID); err != nil {
		t.Fatalf("load initial admin marker: %v", err)
	}
	var accountID string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT id FROM accounts WHERE email = 'admin@example.test'
`).Scan(&accountID); err != nil {
		t.Fatalf("load initialized admin account: %v", err)
	}
	if initialAdminID == "" || initialAdminID != accountID {
		t.Fatalf("initial admin marker = %q, want account %q", initialAdminID, accountID)
	}
	assertInitializationAvailable(false)
}

const apiTestPassword = "CorrectHorse1!"

type sessionResponse struct {
	UserID                 string                  `json:"userId"`
	ExpiresAt              time.Time               `json:"expiresAt"`
	IsAdmin                bool                    `json:"isAdmin"`
	IsInitialAdmin         bool                    `json:"isInitialAdmin"`
	Purpose                identity.SessionPurpose `json:"purpose"`
	RequiresTOTPEnrollment bool                    `json:"requiresTotpEnrollment"`
}

func newAPITestServer(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "api-test.db"),
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
	return db, New(config.Config{
		Storage:           config.StorageConfig{ManagedDir: managedDir},
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
}

func createAPITestAccounts(t *testing.T, db *store.DB) (identity.Account, identity.Account) {
	t.Helper()
	ctx := context.Background()
	svc := identity.New(db.SQL(), identity.Options{ManagedDir: apiTestManagedDir(t, db)})
	secret, err := svc.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("prepare initialization: %v", err)
	}
	initialized, err := svc.Initialize(ctx, identity.InitializationRequest{
		Token:       secret.Token,
		Email:       "admin@example.test",
		DisplayName: "Admin",
		Password:    apiTestPassword,
	})
	if err != nil {
		t.Fatalf("initialize admin: %v", err)
	}
	member, err := svc.CreateAccount(ctx, identity.CreateAccountRequest{
		Email:       "member@example.test",
		DisplayName: "Member",
		Password:    apiTestPassword,
		Role:        domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return initialized.Account, member.Account
}

func apiTestManagedDir(t *testing.T, db *store.DB) string {
	t.Helper()
	var sequence int
	var name, databasePath string
	if err := db.SQL().QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	realDatabasePath, err := filepath.EvalSymlinks(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	managedDir := filepath.Join(filepath.Dir(realDatabasePath), "managed-test")
	if err := os.MkdirAll(managedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return managedDir
}

func issueAPITestSession(t *testing.T, db *store.DB, accountID string) *http.Cookie {
	t.Helper()
	issued, err := identity.New(db.SQL(), identity.Options{}).CreateSession(context.Background(), identity.SessionRequest{
		AccountID: accountID,
		TTL:       time.Hour,
		Purpose:   identity.SessionPurposeFull,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: issued.Token}
}

func authorizedAPITestRequest(t *testing.T, handler http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
