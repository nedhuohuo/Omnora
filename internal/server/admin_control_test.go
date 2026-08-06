package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omnora/internal/identity"
)

func TestAdminCreateUser(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)

	createBody, err := json.Marshal(map[string]any{
		"email":       "new-user@example.test",
		"displayName": "New User",
		"password":    apiTestPassword,
		"role":        "member",
	})
	if err != nil {
		t.Fatalf("marshal create user request: %v", err)
	}

	memberReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(createBody))
	memberReq.Header.Set("Content-Type", "application/json")
	memberReq.AddCookie(memberCookie)
	memberRec := httptest.NewRecorder()
	handler.ServeHTTP(memberRec, memberReq)
	if memberRec.Code != http.StatusForbidden {
		t.Fatalf("create user as non-admin status = %d, want %d, body = %s", memberRec.Code, http.StatusForbidden, memberRec.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(createBody))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.AddCookie(adminCookie)
	adminRec := httptest.NewRecorder()
	handler.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusCreated {
		t.Fatalf("create user as admin status = %d, body = %s", adminRec.Code, adminRec.Body.String())
	}
	var created struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(adminRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}
	if created.User.Email != "new-user@example.test" || created.User.Role != "member" {
		t.Fatalf("created user = %#v, want new-user@example.test/member", created.User)
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/users", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list admin users status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []adminUserDTO `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed admin users: %v", err)
	}
	found := false
	for _, item := range listed.Items {
		if item.ID == created.User.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected newly created user %q in admin user list %#v", created.User.ID, listed.Items)
	}

	svc := identity.New(db.SQL(), identity.Options{})
	if _, err := svc.Authenticate(context.Background(), "new-user@example.test", apiTestPassword); err != nil {
		t.Fatalf("expected new user to authenticate, got error: %v", err)
	}
}

func TestInitialAdminIsProtectedFromAccountAndSpaceMutations(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()

	decodeErrorCode := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
		}
		return body.Error.Code
	}

	disableReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+admin.ID+"/disable", nil)
	disableReq.AddCookie(adminCookie)
	disableRec := httptest.NewRecorder()
	handler.ServeHTTP(disableRec, disableReq)
	if disableRec.Code != http.StatusConflict || decodeErrorCode(disableRec) != "initial_admin_protected" {
		t.Fatalf("disable initial admin status = %d, body = %s", disableRec.Code, disableRec.Body.String())
	}

	var personalSpaceID string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT id FROM spaces WHERE kind = 'personal' AND owner_account_id = ?
`, admin.ID).Scan(&personalSpaceID); err != nil {
		t.Fatalf("load initial admin personal space: %v", err)
	}

	listMembersRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/spaces/"+personalSpaceID+"/members", adminCookie)
	if listMembersRec.Code != http.StatusOK {
		t.Fatalf("list personal space members status = %d, body = %s", listMembersRec.Code, listMembersRec.Body.String())
	}
	var listed struct {
		Items []spaceMemberDTO `json:"items"`
	}
	if err := json.Unmarshal(listMembersRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode personal space members: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].AccountID != admin.ID || !listed.Items[0].Protected {
		t.Fatalf("personal space members = %#v, want protected initial admin", listed.Items)
	}

	permissionBody := bytes.NewReader([]byte(`{"permission":"viewer"}`))
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/"+personalSpaceID+"/members/"+admin.ID, permissionBody)
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(adminCookie)
	putRec := httptest.NewRecorder()
	handler.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusConflict || decodeErrorCode(putRec) != "initial_admin_protected" {
		t.Fatalf("change initial admin permission status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/spaces/"+personalSpaceID+"/members/"+admin.ID, nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict || decodeErrorCode(deleteRec) != "initial_admin_protected" {
		t.Fatalf("remove initial admin status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	var permission string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT permission FROM space_members WHERE space_id = ? AND account_id = ?
`, personalSpaceID, admin.ID).Scan(&permission); err != nil {
		t.Fatalf("load initial admin permission: %v", err)
	}
	if permission != "manager" {
		t.Fatalf("initial admin permission = %q, want manager", permission)
	}
}

func TestAdminPutSpaceMemberResolvesEmailAndRejectsUnknownAccount(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('shared-acl', 'shared', 'ACL Space', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('shared-acl', ?, 'manager')
`, admin.ID); err != nil {
		t.Fatalf("insert owner membership: %v", err)
	}

	body, err := json.Marshal(map[string]string{"permission": "manager"})
	if err != nil {
		t.Fatalf("marshal put member request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/shared-acl/members/"+strings.ToUpper(member.Email), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put member by email status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var added spaceMemberDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode added member: %v", err)
	}
	if added.AccountID != member.ID || added.Email != member.Email || added.DisplayName != member.DisplayName || added.Permission != "manager" {
		t.Fatalf("added member = %#v, want resolved account with manager permission", added)
	}
	var permission string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT permission FROM space_members WHERE space_id = 'shared-acl' AND account_id = ?
`, member.ID).Scan(&permission); err != nil {
		t.Fatalf("query added membership: %v", err)
	}
	if permission != "manager" {
		t.Fatalf("permission = %q, want manager", permission)
	}

	missingReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/shared-acl/members/missing@example.test", bytes.NewReader(body))
	missingReq.Header.Set("Content-Type", "application/json")
	missingReq.AddCookie(adminCookie)
	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("put missing member status = %d, want %d, body = %s", missingRec.Code, http.StatusNotFound, missingRec.Body.String())
	}

	missingSpaceReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/missing-space/members/"+member.ID, bytes.NewReader(body))
	missingSpaceReq.Header.Set("Content-Type", "application/json")
	missingSpaceReq.AddCookie(adminCookie)
	missingSpaceRec := httptest.NewRecorder()
	handler.ServeHTTP(missingSpaceRec, missingSpaceReq)
	if missingSpaceRec.Code != http.StatusNotFound {
		t.Fatalf("put member into missing space status = %d, want %d, body = %s", missingSpaceRec.Code, http.StatusNotFound, missingSpaceRec.Body.String())
	}
}

func TestAuditEventsReturnReadableActorAndTargetLabels(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()

	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO backups(id, status, path, created_by, created_at, notes)
VALUES ('bkp-readable', 'failed', NULL, ?, '2026-08-04T15:00:00Z', 'sqlite online backup')
`, admin.ID); err != nil {
		t.Fatalf("insert backup: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO audit_events(actor_account_id, route_group, action, target_type, target_id, metadata_json)
VALUES
	(?, 'rest', 'admin_user_disable', 'account', ?, '{}'),
	(NULL, 'rest', 'route_group_update', 'route_group', 'mcp', '{}'),
	(?, 'rest', 'admin_backup_create', 'backup', 'bkp-readable', '{}')
`, admin.ID, member.ID, admin.ID); err != nil {
		t.Fatalf("insert audit events: %v", err)
	}

	rec := authorizedAPITestRequest(t, handler, "/api/v1/audit/events?limit=10", adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list audit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Items []auditEventDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode audit events: %v", err)
	}
	if len(listed.Items) < 2 {
		t.Fatalf("audit events = %#v, want inserted rows", listed.Items)
	}
	var accountEvent auditEventDTO
	var systemEvent auditEventDTO
	var backupEvent auditEventDTO
	for _, item := range listed.Items {
		switch item.Action {
		case "admin_user_disable":
			accountEvent = item
		case "route_group_update":
			systemEvent = item
		case "admin_backup_create":
			backupEvent = item
		}
	}
	if accountEvent.Actor != admin.ID || accountEvent.ActorLabel != "Admin" || accountEvent.ActorEmail != admin.Email || accountEvent.ActorDisplayName != admin.DisplayName {
		t.Fatalf("account audit actor fields = %#v, want readable admin labels", accountEvent)
	}
	if accountEvent.TargetID != member.ID || accountEvent.TargetLabel != "Member" {
		t.Fatalf("account audit target fields = %#v, want member target label", accountEvent)
	}
	if systemEvent.ActorLabel != "system" || systemEvent.TargetLabel != "mcp" {
		t.Fatalf("system audit labels = %#v, want system / mcp", systemEvent)
	}
	if backupEvent.TargetID != "bkp-readable" || backupEvent.TargetLabel != "backup 2026-08-04T15:00:00Z" {
		t.Fatalf("backup audit target fields = %#v, want readable backup label", backupEvent)
	}
}
