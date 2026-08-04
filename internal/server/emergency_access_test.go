package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmergencyAccessGrantsViewerRead(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)

	var memberSpaceID string
	if err := db.SQL().QueryRow(`
SELECT id FROM spaces WHERE owner_account_id = ? AND kind = 'personal' AND status = 'active'
`, member.ID).Scan(&memberSpaceID); err != nil {
		t.Fatalf("load member personal space: %v", err)
	}
	root := createTestSpaceAndMount(t, db, "space-ignored", "mnt-emergency", member.ID, "read_only")
	_ = root
	// Re-point a mount onto the member personal space for browse checks.
	if _, err := db.SQL().Exec(`
UPDATE mounts SET space_id = ? WHERE id = 'mnt-emergency'
`, memberSpaceID); err != nil {
		t.Fatalf("move mount to personal space: %v", err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM spaces WHERE id = 'space-ignored'`); err != nil {
		t.Fatalf("cleanup unused space: %v", err)
	}

	denied := authorizedAPITestRequest(t, handler, "/api/v1/spaces/"+memberSpaceID+"/mounts", adminCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("admin mounts before grant status = %d, want 403, body = %s", denied.Code, denied.Body.String())
	}

	body, err := json.Marshal(map[string]string{
		"spaceId":  memberSpaceID,
		"password": apiTestPassword,
		"reason":   "incident review",
	})
	if err != nil {
		t.Fatalf("marshal emergency request: %v", err)
	}
	grantReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/emergency-access", bytes.NewReader(body))
	grantReq.Header.Set("Content-Type", "application/json")
	grantReq.AddCookie(adminCookie)
	grantRec := httptest.NewRecorder()
	handler.ServeHTTP(grantRec, grantReq)
	if grantRec.Code != http.StatusCreated {
		t.Fatalf("create emergency access status = %d, body = %s", grantRec.Code, grantRec.Body.String())
	}

	spacesRec := authorizedAPITestRequest(t, handler, "/api/v1/spaces", adminCookie)
	if spacesRec.Code != http.StatusOK {
		t.Fatalf("list spaces status = %d, body = %s", spacesRec.Code, spacesRec.Body.String())
	}
	var spacesBody struct {
		Items []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"items"`
	}
	if err := json.Unmarshal(spacesRec.Body.Bytes(), &spacesBody); err != nil {
		t.Fatalf("decode spaces: %v", err)
	}
	found := false
	for _, item := range spacesBody.Items {
		if item.ID == memberSpaceID {
			found = true
			if item.Role != "viewer" {
				t.Fatalf("emergency space role = %q, want viewer", item.Role)
			}
		}
	}
	if !found {
		t.Fatalf("expected emergency target space %q in member space list %#v", memberSpaceID, spacesBody.Items)
	}

	allowed := authorizedAPITestRequest(t, handler, "/api/v1/spaces/"+memberSpaceID+"/mounts", adminCookie)
	if allowed.Code != http.StatusOK {
		t.Fatalf("admin mounts after grant status = %d, body = %s", allowed.Code, allowed.Body.String())
	}

	// A different admin session must not reuse the grant.
	otherCookie := issueAPITestSession(t, db, admin.ID)
	otherDenied := authorizedAPITestRequest(t, handler, "/api/v1/spaces/"+memberSpaceID+"/mounts", otherCookie)
	if otherDenied.Code != http.StatusForbidden {
		t.Fatalf("other session mounts status = %d, want 403, body = %s", otherDenied.Code, otherDenied.Body.String())
	}
}
