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
