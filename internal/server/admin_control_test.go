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
