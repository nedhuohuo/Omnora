package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func newTeamSpaceAPITestServer(t *testing.T) (*store.DB, http.Handler, string) {
	t.Helper()
	managedRoot, err := os.MkdirTemp("/private/tmp", "omnora-team-storage-")
	if err != nil {
		t.Fatalf("create managed root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(managedRoot) })
	return newTeamSpaceAPITestServerWithManagedRoot(t, managedRoot)
}

func newTeamSpaceAPITestServerWithManagedRoot(t *testing.T, managedRoot string) (*store.DB, http.Handler, string) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "team-spaces.db"), BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery ready: %v", err)
	}
	handler := New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage:           config.StorageConfig{ManagedDir: managedRoot},
	}, db)
	return db, handler, managedRoot
}

func teamSpaceErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, rec.Body.String())
	}
	return response.Error.Code
}

func TestMemberCreatesTeamSpaceWithManagedMount(t *testing.T) {
	db, handler, managedRoot := newTeamSpaceAPITestServer(t)
	_, member := createAPITestAccounts(t, db)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":" 研发协作 "}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, member.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team space status = %d body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		Space struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"space"`
		Mount struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Mode string `json:"mode"`
		} `json:"mount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Space.ID == "" || created.Space.Type != "shared" || created.Space.Name != "研发协作" || created.Space.Role != "manager" {
		t.Fatalf("created space = %#v, want managed shared space", created.Space)
	}
	if created.Mount.ID == "" || created.Mount.Kind != "managed" || created.Mount.Mode != "read-write" {
		t.Fatalf("created mount = %#v, want managed read-write mount", created.Mount)
	}

	var ownerID, mountRoot, identityJSON, permission, auditAction string
	if err := db.SQL().QueryRow(`SELECT owner_account_id FROM spaces WHERE id = ?`, created.Space.ID).Scan(&ownerID); err != nil {
		t.Fatalf("load team space owner: %v", err)
	}
	if err := db.SQL().QueryRow(`SELECT root_path, COALESCE(mount_identity_json, '') FROM mounts WHERE id = ?`, created.Mount.ID).Scan(&mountRoot, &identityJSON); err != nil {
		t.Fatalf("load team mount: %v", err)
	}
	if err := db.SQL().QueryRow(`SELECT permission FROM space_members WHERE space_id = ? AND account_id = ?`, created.Space.ID, member.ID).Scan(&permission); err != nil {
		t.Fatalf("load creator membership: %v", err)
	}
	if err := db.SQL().QueryRow(`SELECT action FROM audit_events WHERE target_type = 'space' AND target_id = ? ORDER BY occurred_at DESC LIMIT 1`, created.Space.ID).Scan(&auditAction); err != nil {
		t.Fatalf("load create audit: %v", err)
	}
	if ownerID != member.ID || permission != "manager" {
		t.Fatalf("owner/membership = %q/%q, want %q/manager", ownerID, permission, member.ID)
	}
	if want := filepath.Join(managedRoot, "team-spaces", created.Space.ID); mountRoot != want {
		t.Fatalf("managed mount root = %q, want %q", mountRoot, want)
	}
	if info, err := os.Stat(mountRoot); err != nil || !info.IsDir() {
		t.Fatalf("managed mount directory stat = %v, info=%#v", err, info)
	}
	if identityJSON == "" || auditAction != "team_space_create" {
		t.Fatalf("identity/audit = %q/%q, want captured identity and team_space_create", identityJSON, auditAction)
	}
}

func TestTeamSpaceCreateRejectsInvalidOrUnauthorizedRequests(t *testing.T) {
	db, handler, _ := newTeamSpaceAPITestServer(t)
	_, member := createAPITestAccounts(t, db)

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":"Team"}`))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized || teamSpaceErrorCode(t, unauthorizedRec) != "unauthorized" {
		t.Fatalf("unauthorized create = %d body=%s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}

	cookie := issueAPITestSession(t, db, member.ID)
	invalid := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":"bad\u0001name"}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalid.AddCookie(cookie)
	invalidRec := httptest.NewRecorder()
	handler.ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest || teamSpaceErrorCode(t, invalidRec) != "invalid_input" {
		t.Fatalf("invalid create = %d body=%s", invalidRec.Code, invalidRec.Body.String())
	}

	if _, err := db.SQL().Exec(`UPDATE accounts SET status = 'disabled' WHERE id = ?`, member.ID); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	disabled := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":"Team"}`))
	disabled.Header.Set("Content-Type", "application/json")
	disabled.AddCookie(cookie)
	disabledRec := httptest.NewRecorder()
	handler.ServeHTTP(disabledRec, disabled)
	if disabledRec.Code != http.StatusUnauthorized || teamSpaceErrorCode(t, disabledRec) != "unauthorized" {
		t.Fatalf("disabled create = %d body=%s", disabledRec.Code, disabledRec.Body.String())
	}
}

func TestTeamSpaceCreateRejectsUnsafeOrOverlappingManagedStorage(t *testing.T) {
	unsafeDB, unsafeHandler, _ := newTeamSpaceAPITestServerWithManagedRoot(t, "/")
	_, unsafeMember := createAPITestAccounts(t, unsafeDB)
	unsafeReq := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":"Unsafe"}`))
	unsafeReq.Header.Set("Content-Type", "application/json")
	unsafeReq.AddCookie(issueAPITestSession(t, unsafeDB, unsafeMember.ID))
	unsafeRec := httptest.NewRecorder()
	unsafeHandler.ServeHTTP(unsafeRec, unsafeReq)
	if unsafeRec.Code != http.StatusServiceUnavailable || teamSpaceErrorCode(t, unsafeRec) != "managed_storage_unavailable" {
		t.Fatalf("unsafe storage create = %d body=%s", unsafeRec.Code, unsafeRec.Body.String())
	}

	db, handler, managedRoot := newTeamSpaceAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	var personalSpaceID string
	if err := db.SQL().QueryRow(`SELECT id FROM spaces WHERE kind = 'personal' AND owner_account_id = ?`, admin.ID).Scan(&personalSpaceID); err != nil {
		t.Fatalf("load admin personal space: %v", err)
	}
	identity, err := mountid.Capture(managedRoot)
	if err != nil {
		t.Fatalf("capture managed root: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal managed root identity: %v", err)
	}
	if _, err := db.SQL().Exec(`
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES ('managed-root-conflict', ?, 'Managed root', ?, 'managed', 'read_write', 0, 'active', ?)
`, personalSpaceID, managedRoot, string(identityJSON)); err != nil {
		t.Fatalf("insert conflicting managed root: %v", err)
	}
	overlapReq := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":"Overlap"}`))
	overlapReq.Header.Set("Content-Type", "application/json")
	overlapReq.AddCookie(issueAPITestSession(t, db, member.ID))
	overlapRec := httptest.NewRecorder()
	handler.ServeHTTP(overlapRec, overlapReq)
	if overlapRec.Code != http.StatusServiceUnavailable || teamSpaceErrorCode(t, overlapRec) != "managed_storage_unavailable" {
		t.Fatalf("overlap storage create = %d body=%s", overlapRec.Code, overlapRec.Body.String())
	}
}

func TestTeamSpaceCreateRollsBackEmptyDirectoryOnDatabaseFailure(t *testing.T) {
	db, handler, managedRoot := newTeamSpaceAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`
CREATE TRIGGER fail_team_mount
BEFORE INSERT ON mounts
WHEN NEW.kind = 'managed'
BEGIN
  SELECT RAISE(ABORT, 'forced team mount failure');
END;
`); err != nil {
		t.Fatalf("create failing mount trigger: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":"Rollback"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, member.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("rollback create = %d body=%s", rec.Code, rec.Body.String())
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM spaces WHERE name = 'Rollback'`).Scan(&count); err != nil {
		t.Fatalf("count rollback spaces: %v", err)
	}
	if count != 0 {
		t.Fatalf("rollback space count = %d, want 0", count)
	}
	entries, err := os.ReadDir(filepath.Join(managedRoot, "team-spaces"))
	if err != nil {
		t.Fatalf("read managed team directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rollback left team directories: %#v", entries)
	}
}

func createTeamSpaceForTest(t *testing.T, handler http.Handler, cookie *http.Cookie, name string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", bytes.NewBufferString(`{"name":`+strconv.Quote(name)+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team space %q = %d body=%s", name, rec.Code, rec.Body.String())
	}
	var response struct {
		Space struct {
			ID string `json:"id"`
		} `json:"space"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode created team space: %v", err)
	}
	if response.Space.ID == "" {
		t.Fatal("created team space has no id")
	}
	return response.Space.ID
}

func teamSpaceRequest(t *testing.T, handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestTeamSpaceManagerManagesActiveMembers(t *testing.T) {
	db, handler, _ := newTeamSpaceAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	identityService := identity.New(db.SQL(), identity.Options{})
	other, err := identityService.CreateAccount(context.Background(), identity.CreateAccountRequest{
		Email: "other@example.test", DisplayName: "Other", Password: apiTestPassword, Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatalf("create other account: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)
	spaceID := createTeamSpaceForTest(t, handler, adminCookie, "Product team")

	put := teamSpaceRequest(t, handler, http.MethodPut, "/api/v1/team-spaces/"+spaceID+"/members/MEMBER%40EXAMPLE.TEST", `{"permission":"viewer"}`, adminCookie)
	if put.Code != http.StatusOK {
		t.Fatalf("add team member = %d body=%s", put.Code, put.Body.String())
	}
	var added spaceMemberDTO
	if err := json.Unmarshal(put.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode added member: %v", err)
	}
	if added.AccountID != member.ID || added.Permission != "viewer" || added.Email != member.Email {
		t.Fatalf("added member = %#v", added)
	}

	updated := teamSpaceRequest(t, handler, http.MethodPut, "/api/v1/team-spaces/"+spaceID+"/members/"+member.ID, `{"permission":"editor"}`, adminCookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("update team member = %d body=%s", updated.Code, updated.Body.String())
	}
	list := teamSpaceRequest(t, handler, http.MethodGet, "/api/v1/team-spaces/"+spaceID+"/members", "", adminCookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), member.Email) || !strings.Contains(list.Body.String(), `"permission":"editor"`) {
		t.Fatalf("manager list = %d body=%s", list.Code, list.Body.String())
	}

	editorDenied := teamSpaceRequest(t, handler, http.MethodGet, "/api/v1/team-spaces/"+spaceID+"/members", "", issueAPITestSession(t, db, member.ID))
	if editorDenied.Code != http.StatusForbidden || teamSpaceErrorCode(t, editorDenied) != "forbidden" {
		t.Fatalf("editor member list = %d body=%s", editorDenied.Code, editorDenied.Body.String())
	}
	otherSpaceID := createTeamSpaceForTest(t, handler, issueAPITestSession(t, db, other.Account.ID), "Other team")
	if otherSpaceID == spaceID {
		t.Fatal("team spaces must have distinct ids")
	}
	otherManagerDenied := teamSpaceRequest(t, handler, http.MethodGet, "/api/v1/team-spaces/"+spaceID+"/members", "", issueAPITestSession(t, db, other.Account.ID))
	if otherManagerDenied.Code != http.StatusForbidden || teamSpaceErrorCode(t, otherManagerDenied) != "forbidden" {
		t.Fatalf("other-space manager list = %d body=%s", otherManagerDenied.Code, otherManagerDenied.Body.String())
	}

	deleted := teamSpaceRequest(t, handler, http.MethodDelete, "/api/v1/team-spaces/"+spaceID+"/members/"+member.ID, "", adminCookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("remove team member = %d body=%s", deleted.Code, deleted.Body.String())
	}
	var auditCount int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM audit_events WHERE target_id = ? AND action IN ('team_space_member_set', 'team_space_member_remove')`, spaceID).Scan(&auditCount); err != nil {
		t.Fatalf("count membership audits: %v", err)
	}
	if auditCount != 3 {
		t.Fatalf("membership audit count = %d, want 3", auditCount)
	}
}

func TestTeamSpaceMemberManagementProtectsPersonalSpacesAndLastManager(t *testing.T) {
	db, handler, _ := newTeamSpaceAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	spaceID := createTeamSpaceForTest(t, handler, adminCookie, "One manager")
	inactive, err := identity.New(db.SQL(), identity.Options{}).CreateAccount(context.Background(), identity.CreateAccountRequest{
		Email: "inactive@example.test", DisplayName: "Inactive", Password: apiTestPassword, Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatalf("create inactive account: %v", err)
	}
	if _, err := db.SQL().Exec(`UPDATE accounts SET status = 'disabled' WHERE id = ?`, inactive.Account.ID); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/v1/team-spaces/" + spaceID + "/members/unknown%40example.test", `{"permission":"viewer"}`},
		{http.MethodPut, "/api/v1/team-spaces/" + spaceID + "/members/inactive%40example.test", `{"permission":"viewer"}`},
		{http.MethodPut, "/api/v1/team-spaces/" + spaceID + "/members/" + admin.ID, `{"permission":"viewer"}`},
		{http.MethodDelete, "/api/v1/team-spaces/" + spaceID + "/members/" + admin.ID, ""},
	} {
		rec := teamSpaceRequest(t, handler, request.method, request.path, request.body, adminCookie)
		if strings.HasSuffix(request.path, "unknown%40example.test") || strings.HasSuffix(request.path, "inactive%40example.test") {
			if rec.Code != http.StatusNotFound || teamSpaceErrorCode(t, rec) != "not_found" {
				t.Fatalf("unknown member operation = %d body=%s", rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusConflict || teamSpaceErrorCode(t, rec) != "last_manager_required" {
			t.Fatalf("last manager operation = %d body=%s", rec.Code, rec.Body.String())
		}
	}

	var personalSpaceID string
	if err := db.SQL().QueryRow(`SELECT id FROM spaces WHERE kind = 'personal' AND owner_account_id = ?`, admin.ID).Scan(&personalSpaceID); err != nil {
		t.Fatalf("load personal space: %v", err)
	}
	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/team-spaces/" + personalSpaceID + "/members", ""},
		{http.MethodPut, "/api/v1/team-spaces/" + personalSpaceID + "/members/" + admin.ID, `{"permission":"manager"}`},
		{http.MethodDelete, "/api/v1/team-spaces/" + personalSpaceID + "/members/" + admin.ID, ""},
	} {
		rec := teamSpaceRequest(t, handler, request.method, request.path, request.body, adminCookie)
		if rec.Code != http.StatusConflict || teamSpaceErrorCode(t, rec) != "personal_space_protected" {
			t.Fatalf("personal space management = %d body=%s", rec.Code, rec.Body.String())
		}
	}
}
