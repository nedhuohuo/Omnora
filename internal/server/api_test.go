package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func TestSessionResponsesIncludeCurrentAdminStatus(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)

	for _, tc := range []struct {
		name      string
		email     string
		wantAdmin bool
		wantID    string
	}{
		{name: "admin", email: admin.Email, wantAdmin: true, wantID: admin.ID},
		{name: "member", email: member.Email, wantAdmin: false, wantID: member.ID},
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
			if loginResponse.UserID != tc.wantID || loginResponse.IsAdmin != tc.wantAdmin || loginResponse.ExpiresAt.IsZero() {
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
			if currentResponse.UserID != tc.wantID || currentResponse.IsAdmin != tc.wantAdmin || currentResponse.ExpiresAt.IsZero() {
				t.Fatalf("current session response = %#v", currentResponse)
			}
		})
	}
}

func TestAdminLoginWithoutTOTPCreatesEnrollmentSession(t *testing.T) {
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
	if response.Purpose != identity.SessionPurposeTOTPEnrollment || !response.RequiresTOTPEnrollment {
		t.Fatalf("login response = %#v, want enrollment session", response)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %#v", cookies)
	}
	session, err := identity.New(db.SQL(), identity.Options{}).VerifySession(context.Background(), cookies[0].Value)
	if err != nil {
		t.Fatalf("verify enrollment session: %v", err)
	}
	if session.Purpose != identity.SessionPurposeTOTPEnrollment {
		t.Fatalf("session purpose = %q, want enrollment", session.Purpose)
	}
}

func TestPasswordResetRequiredIsCompletedDuringLogin(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`UPDATE accounts SET password_reset_required = 1 WHERE id = ?`, member.ID); err != nil {
		t.Fatalf("mark password reset: %v", err)
	}
	login := func(newPassword string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{"login": member.Email, "password": apiTestPassword, "newPassword": newPassword})
		if err != nil {
			t.Fatalf("marshal login: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	missing := login("")
	if missing.Code != http.StatusUnauthorized || !strings.Contains(missing.Body.String(), `"code":"password_reset_required"`) {
		t.Fatalf("missing new password response = %d %s", missing.Code, missing.Body.String())
	}
	success := login("NewCorrectHorse2!")
	if success.Code != http.StatusCreated {
		t.Fatalf("reset login status = %d, body = %s", success.Code, success.Body.String())
	}
	var resetRequired int
	if err := db.SQL().QueryRow(`SELECT password_reset_required FROM accounts WHERE id = ?`, member.ID).Scan(&resetRequired); err != nil {
		t.Fatalf("query reset flag: %v", err)
	}
	if resetRequired != 0 {
		t.Fatalf("password_reset_required = %d, want 0", resetRequired)
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

func TestAdminSpaceAndMountListingsRequireAdminAndReturnGlobalData(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	ctx := context.Background()

	_, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES
	('shared-active', 'shared', 'All Hands', ?, 'active'),
	('shared-disabled', 'shared', 'Archived', ?, 'disabled')
`, admin.ID, admin.ID)
	if err != nil {
		t.Fatalf("insert spaces: %v", err)
	}
	_, err = db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status)
VALUES
	('mount-active', 'shared-active', 'Docs', '/srv/docs', 'managed', 'read_write', 1, 'active'),
	('mount-disabled-space', 'shared-disabled', 'Legacy', '/srv/legacy', 'external', 'read_only', 0, 'disabled'),
	('mount-deleted', 'shared-active', 'Deleted', '/srv/deleted', 'managed', 'read_only', 0, 'deleted')
`)
	if err != nil {
		t.Fatalf("insert mounts: %v", err)
	}

	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)
	for _, path := range []string{"/api/v1/admin/spaces", "/api/v1/admin/mounts"} {
		rec := authorizedAPITestRequest(t, handler, path, memberCookie)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("member GET %s status = %d, want %d", path, rec.Code, http.StatusForbidden)
		}
	}

	spacesRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/spaces", adminCookie)
	if spacesRec.Code != http.StatusOK {
		t.Fatalf("admin spaces status = %d, body = %s", spacesRec.Code, spacesRec.Body.String())
	}
	var spaces struct {
		Items []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(spacesRec.Body.Bytes(), &spaces); err != nil {
		t.Fatalf("decode spaces: %v", err)
	}
	if !containsAdminSpace(spaces.Items, "shared-active", "shared", "All Hands") {
		t.Fatalf("active global space missing: %#v", spaces.Items)
	}
	if containsAdminSpace(spaces.Items, "shared-disabled", "shared", "Archived") {
		t.Fatalf("disabled space must not be listed: %#v", spaces.Items)
	}

	mountsRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/mounts", adminCookie)
	if mountsRec.Code != http.StatusOK {
		t.Fatalf("admin mounts status = %d, body = %s", mountsRec.Code, mountsRec.Body.String())
	}
	var mounts struct {
		Items []mountDTO `json:"items"`
	}
	if err := json.Unmarshal(mountsRec.Body.Bytes(), &mounts); err != nil {
		t.Fatalf("decode mounts: %v", err)
	}
	if !containsAdminMount(mounts.Items, "mount-active", "Docs", "All Hands", "read-write", "indexed", "active") {
		t.Fatalf("active mount missing or malformed: %#v", mounts.Items)
	}
	if !containsAdminMount(mounts.Items, "mount-disabled-space", "Legacy", "Archived", "read-only", "not indexed", "disabled") {
		t.Fatalf("non-deleted disabled mount missing: %#v", mounts.Items)
	}
	if containsAdminMountID(mounts.Items, "mount-deleted") {
		t.Fatalf("deleted mount must not be listed: %#v", mounts.Items)
	}
}

func TestEnqueueIndexJobRejectsNonIndexableMounts(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	ctx := context.Background()
	_, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('shared-active', 'shared', 'All Hands', ?, 'active')
`, admin.ID)
	if err != nil {
		t.Fatalf("insert space: %v", err)
	}
	_, err = db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status)
VALUES
	('inactive', 'shared-active', 'Inactive', '/tmp/inactive', 'external', 'read_only', 1, 'disabled'),
	('no-index', 'shared-active', 'No index', '/tmp/no-index', 'external', 'read_only', 0, 'active')
`)
	if err != nil {
		t.Fatalf("insert mounts: %v", err)
	}
	cookie := issueAPITestSession(t, db, admin.ID)
	for _, tc := range []struct {
		name    string
		mountID string
		want    int
	}{
		{name: "missing", mountID: "missing", want: http.StatusNotFound},
		{name: "inactive", mountID: "inactive", want: http.StatusConflict},
		{name: "index disabled", mountID: "no-index", want: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"mountId":"` + tc.mountID + `"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/index-jobs", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestIndexJobResumesFromCheckpointWithoutIncreasingAttempts(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	ctx := context.Background()
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".index-job-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for i := range 501 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d.txt", i)), []byte("content"), 0o600); err != nil {
			t.Fatalf("write indexed file: %v", err)
		}
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture mount identity: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal mount identity: %v", err)
	}
	_, err = db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('shared-active', 'shared', 'All Hands', ?, 'active')
`, admin.ID)
	if err != nil {
		t.Fatalf("insert space: %v", err)
	}
	_, err = db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES ('indexable', 'shared-active', 'Indexable', ?, 'external', 'read_only', 1, 'active', ?)
`, root, string(identityJSON))
	if err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	cookie := issueAPITestSession(t, db, admin.ID)
	enqueue := httptest.NewRequest(http.MethodPost, "/api/v1/admin/index-jobs", bytes.NewReader([]byte(`{"mountId":"indexable"}`)))
	enqueue.Header.Set("Content-Type", "application/json")
	enqueue.AddCookie(cookie)
	enqueueRec := httptest.NewRecorder()
	handler.ServeHTTP(enqueueRec, enqueue)
	if enqueueRec.Code != http.StatusCreated {
		t.Fatalf("enqueue status = %d, body = %s", enqueueRec.Code, enqueueRec.Body.String())
	}
	var created struct {
		ID        string `json:"id"`
		SpaceName string `json:"spaceName"`
		MountName string `json:"mountName"`
	}
	if err := json.Unmarshal(enqueueRec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created job: id = %q, err = %v, body = %s", created.ID, err, enqueueRec.Body.String())
	}
	if created.SpaceName != "All Hands" || created.MountName != "Indexable" {
		t.Fatalf("created job labels = %#v, want All Hands / Indexable", created)
	}
	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/index-jobs", cookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list index jobs status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []struct {
			SpaceName string `json:"spaceName"`
			MountName string `json:"mountName"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode index jobs: %v", err)
	}
	if len(listed.Items) == 0 || listed.Items[0].SpaceName != "All Hands" || listed.Items[0].MountName != "Indexable" {
		t.Fatalf("index job labels = %#v, want All Hands / Indexable", listed.Items)
	}

	run := func() map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/index-jobs/"+created.ID+"/run", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("run status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		return result
	}

	first := run()
	if first["Done"] != false {
		t.Fatalf("first run result = %#v, want yielded batch", first)
	}
	var status string
	var attempts int
	var checkpoint string
	if err := db.SQL().QueryRowContext(ctx, "SELECT status, attempts, checkpoint_json FROM jobs WHERE id = ?", created.ID).Scan(&status, &attempts, &checkpoint); err != nil {
		t.Fatalf("load yielded job: %v", err)
	}
	if status != "queued" || attempts != 0 || checkpoint == "{}" {
		t.Fatalf("yielded job = status %q attempts %d checkpoint %s", status, attempts, checkpoint)
	}

	second := run()
	if second["Done"] != true {
		t.Fatalf("second run result = %#v, want completed batch", second)
	}
	if err := db.SQL().QueryRowContext(ctx, "SELECT status, attempts FROM jobs WHERE id = ?", created.ID).Scan(&status, &attempts); err != nil {
		t.Fatalf("load completed job: %v", err)
	}
	if status != "completed" || attempts != 0 {
		t.Fatalf("completed job = status %q attempts %d, want completed with zero attempts", status, attempts)
	}
	var entries int
	if err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(1) FROM catalog_entries WHERE mount_id = 'indexable'").Scan(&entries); err != nil {
		t.Fatalf("count catalog entries: %v", err)
	}
	if entries != 501 {
		t.Fatalf("catalog entry count = %d, want 501", entries)
	}
}

const apiTestPassword = "CorrectHorse1!"

type sessionResponse struct {
	UserID                 string                  `json:"userId"`
	ExpiresAt              time.Time               `json:"expiresAt"`
	IsAdmin                bool                    `json:"isAdmin"`
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
	return db, New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
}

func createAPITestAccounts(t *testing.T, db *store.DB) (identity.Account, identity.Account) {
	t.Helper()
	ctx := context.Background()
	svc := identity.New(db.SQL(), identity.Options{})
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

func issueAPITestSession(t *testing.T, db *store.DB, accountID string) *http.Cookie {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(context.Background(), `
UPDATE accounts
SET totp_required = CASE WHEN role = 'admin' THEN 1 ELSE totp_required END,
    totp_confirmed_at = CASE WHEN role = 'admin' THEN COALESCE(totp_confirmed_at, ?) ELSE totp_confirmed_at END
WHERE id = ?
`, now, accountID); err != nil {
		t.Fatalf("prepare test admin TOTP state: %v", err)
	}
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

func containsAdminSpace(items []struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}, id, kind, name string) bool {
	for _, item := range items {
		if item.ID == id && item.Type == kind && item.Name == name {
			return true
		}
	}
	return false
}

func containsAdminMount(items []mountDTO, id, name, space, mode, index, health string) bool {
	for _, item := range items {
		if item.ID == id && item.Name == name && item.Space == space && item.Mode == mode && item.Index == index && item.Health == health {
			return true
		}
	}
	return false
}

func containsAdminMountID(items []mountDTO, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
