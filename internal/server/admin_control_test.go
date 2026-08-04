package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
