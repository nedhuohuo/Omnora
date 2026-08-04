package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestListAndRevokeShares(t *testing.T) {
	db, handler := newShareAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-1", "mount-1", admin.ID, "read_write")
	if err := os.WriteFile(filepath.Join(root, "report.pdf"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write shared file: %v", err)
	}
	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)

	createBody, err := json.Marshal(map[string]any{
		"spaceId":      "space-1",
		"mountId":      "mount-1",
		"relativePath": "report.pdf",
		"maxDownloads": 2,
	})
	if err != nil {
		t.Fatalf("marshal create share request: %v", err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create share status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created share: id = %q, err = %v, body = %s", created.ID, err, createRec.Body.String())
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/shares", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list shares status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []shareRecordDTO `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed shares: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("listed shares = %#v, want exactly the created share", listed.Items)
	}
	if listed.Items[0].MaxDownloads == nil || *listed.Items[0].MaxDownloads != 2 {
		t.Fatalf("listed share maxDownloads = %#v, want 2", listed.Items[0].MaxDownloads)
	}
	if listed.Items[0].Status != "active" {
		t.Fatalf("listed share status = %q, want active", listed.Items[0].Status)
	}
	if listed.Items[0].SpaceName != "Test Space" || listed.Items[0].MountName != "Docs" {
		t.Fatalf("listed share location = %q / %q, want Test Space / Docs", listed.Items[0].SpaceName, listed.Items[0].MountName)
	}
	if listed.Items[0].CreatorDisplayName != "Admin" || listed.Items[0].CreatorEmail != "admin@example.test" {
		t.Fatalf("listed share creator = %q / %q, want Admin / admin@example.test", listed.Items[0].CreatorDisplayName, listed.Items[0].CreatorEmail)
	}

	memberListRec := authorizedAPITestRequest(t, handler, "/api/v1/shares", memberCookie)
	if memberListRec.Code != http.StatusOK {
		t.Fatalf("member list shares status = %d, body = %s", memberListRec.Code, memberListRec.Body.String())
	}
	var memberListed struct {
		Items []shareRecordDTO `json:"items"`
	}
	if err := json.Unmarshal(memberListRec.Body.Bytes(), &memberListed); err != nil {
		t.Fatalf("decode member listed shares: %v", err)
	}
	if len(memberListed.Items) != 0 {
		t.Fatalf("member (not creator or manager) should not see the share: %#v", memberListed.Items)
	}

	revokeAsMember := httptest.NewRequest(http.MethodDelete, "/api/v1/shares/"+created.ID, nil)
	revokeAsMember.AddCookie(memberCookie)
	revokeAsMemberRec := httptest.NewRecorder()
	handler.ServeHTTP(revokeAsMemberRec, revokeAsMember)
	if revokeAsMemberRec.Code != http.StatusForbidden {
		t.Fatalf("revoke as non-owner status = %d, want %d", revokeAsMemberRec.Code, http.StatusForbidden)
	}

	revokeAsAdmin := httptest.NewRequest(http.MethodDelete, "/api/v1/shares/"+created.ID, nil)
	revokeAsAdmin.AddCookie(adminCookie)
	revokeAsAdminRec := httptest.NewRecorder()
	handler.ServeHTTP(revokeAsAdminRec, revokeAsAdmin)
	if revokeAsAdminRec.Code != http.StatusNoContent {
		t.Fatalf("revoke as creator status = %d, body = %s", revokeAsAdminRec.Code, revokeAsAdminRec.Body.String())
	}

	afterRevokeRec := authorizedAPITestRequest(t, handler, "/api/v1/shares", adminCookie)
	var afterRevoke struct {
		Items []shareRecordDTO `json:"items"`
	}
	if err := json.Unmarshal(afterRevokeRec.Body.Bytes(), &afterRevoke); err != nil {
		t.Fatalf("decode shares after revoke: %v", err)
	}
	if len(afterRevoke.Items) != 1 || afterRevoke.Items[0].Status != "revoked" {
		t.Fatalf("shares after revoke = %#v, want one revoked share", afterRevoke.Items)
	}
}
