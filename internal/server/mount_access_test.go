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

func TestMountWhitelistGrantLifecycleAndNoAdminContentBypass(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	createTestSpaceAndMount(t, db, "space-1", "mount-1", admin.ID, "read_write")
	addSpaceMember(t, db, "space-1", member.ID, "editor")
	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)

	assertMountListCount(t, handler, memberCookie, 0)
	assertSessionContentAccess(t, handler, memberCookie, false)

	putGrant := func(accountID, permission string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"permission": permission})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/mounts/mount-1/grants/"+accountID, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := putGrant(member.ID, "editor"); rec.Code != http.StatusOK {
		t.Fatalf("put member grant status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertMountListCount(t, handler, memberCookie, 1)
	assertSessionContentAccess(t, handler, memberCookie, true)
	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mounts/mount-1", nil)
	detailReq.AddCookie(adminCookie)
	detailRec := httptest.NewRecorder()
	handler.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("mount detail status = %d, body = %s", detailRec.Code, detailRec.Body.String())
	}
	var detail adminMountDetailDTO
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "mount-1" || detail.GrantCount != 2 || !detail.AllowPublicShares {
		t.Fatalf("mount detail = %#v", detail)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/spaces/space-1/mounts/mount-1/directories", bytes.NewReader([]byte(`{"parentPath":".","name":"created"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(memberCookie)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create directory status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mounts/mount-1/grants/"+admin.ID, nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete admin grant status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	childrenReq := httptest.NewRequest(http.MethodGet, "/api/v1/spaces/space-1/mounts/mount-1/children", nil)
	childrenReq.AddCookie(adminCookie)
	childrenRec := httptest.NewRecorder()
	handler.ServeHTTP(childrenRec, childrenReq)
	if childrenRec.Code != http.StatusForbidden {
		t.Fatalf("admin content without grant status = %d, body = %s", childrenRec.Code, childrenRec.Body.String())
	}

	grantsReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mounts/mount-1/grants", nil)
	grantsReq.AddCookie(adminCookie)
	grantsRec := httptest.NewRecorder()
	handler.ServeHTTP(grantsRec, grantsReq)
	if grantsRec.Code != http.StatusOK {
		t.Fatalf("list grants status = %d, body = %s", grantsRec.Code, grantsRec.Body.String())
	}
	var grants struct {
		Items []adminMountGrantDTO `json:"items"`
	}
	if err := json.Unmarshal(grantsRec.Body.Bytes(), &grants); err != nil {
		t.Fatal(err)
	}
	if len(grants.Items) != 1 || grants.Items[0].AccountID != member.ID || grants.Items[0].EffectivePermission != "editor" {
		t.Fatalf("grants = %#v", grants.Items)
	}
}

func TestMemberMountResponsesExposeEffectiveCapabilities(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	root := createTestSpaceAndMount(t, db, "space-1", "mount-1", admin.ID, "read_write")
	addSpaceMember(t, db, "space-1", member.ID, "editor")
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO mount_account_grants(mount_id, account_id, permission) VALUES ('mount-1', ?, 'viewer')`, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
INSERT INTO catalog_entries(id, space_id, mount_id, relative_path, name, entry_kind, preview_kind, size_bytes, modified_at, identity_fingerprint)
VALUES ('entry-visible', 'space-1', 'mount-1', 'visible.txt', 'visible.txt', 'file', 'text', 7, '2026-01-01T00:00:00Z', 'fp-visible')
`); err != nil {
		t.Fatal(err)
	}
	cookie := issueAPITestSession(t, db, member.ID)

	mountsRec := authorizedAPITestRequest(t, handler, "/api/v1/spaces/space-1/mounts", cookie)
	var mounts struct {
		Items []mountDTO `json:"items"`
	}
	if mountsRec.Code != http.StatusOK || json.Unmarshal(mountsRec.Body.Bytes(), &mounts) != nil || len(mounts.Items) != 1 {
		t.Fatalf("mount response status=%d body=%s", mountsRec.Code, mountsRec.Body.String())
	}
	if mounts.Items[0].EffectivePermission != "viewer" || mounts.Items[0].CanWrite || mounts.Items[0].CanShare || !mounts.Items[0].ReadOnly {
		t.Fatalf("mount capabilities = %#v", mounts.Items[0])
	}

	childrenReq := httptest.NewRequest(http.MethodGet, "/api/v1/spaces/space-1/mounts/mount-1/children", nil)
	childrenReq.AddCookie(cookie)
	childrenRec := httptest.NewRecorder()
	handler.ServeHTTP(childrenRec, childrenReq)
	var children struct {
		ReadOnly            bool   `json:"readOnly"`
		EffectivePermission string `json:"effectivePermission"`
		CanWrite            bool   `json:"canWrite"`
		CanShare            bool   `json:"canShare"`
		Entries             []struct {
			ReadOnly bool `json:"readOnly"`
		} `json:"entries"`
	}
	if childrenRec.Code != http.StatusOK || json.Unmarshal(childrenRec.Body.Bytes(), &children) != nil {
		t.Fatalf("children response status=%d body=%s", childrenRec.Code, childrenRec.Body.String())
	}
	if !children.ReadOnly || children.EffectivePermission != "viewer" || children.CanWrite || children.CanShare || len(children.Entries) != 1 || !children.Entries[0].ReadOnly {
		t.Fatalf("children capabilities = %#v", children)
	}

	searchRec := authorizedAPITestRequest(t, handler, "/api/v1/spaces/space-1/search?q=visible", cookie)
	var search struct {
		Items []struct {
			EffectivePermission string `json:"effectivePermission"`
			CanWrite            bool   `json:"canWrite"`
			CanShare            bool   `json:"canShare"`
		} `json:"items"`
	}
	if searchRec.Code != http.StatusOK || json.Unmarshal(searchRec.Body.Bytes(), &search) != nil || len(search.Items) != 1 {
		t.Fatalf("search response status=%d body=%s", searchRec.Code, searchRec.Body.String())
	}
	if search.Items[0].EffectivePermission != "viewer" || search.Items[0].CanWrite || search.Items[0].CanShare {
		t.Fatalf("search capabilities = %#v", search.Items[0])
	}
}

func TestGrantPutRejectsMissingMembershipAndRemovesStaleGrant(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	createTestSpaceAndMount(t, db, "space-1", "mount-1", admin.ID, "read_write")
	if _, err := db.SQL().Exec(`INSERT INTO mount_account_grants(mount_id, account_id, permission) VALUES ('mount-1', ?, 'manager')`, member.ID); err != nil {
		t.Fatal(err)
	}
	fixtures := []string{
		`INSERT INTO shares(id, public_id, secret_hash, creator_account_id, space_id, mount_id, relative_path, expires_at) VALUES ('stale-share', 'stale-public', 'hash', ?, 'space-1', 'mount-1', 'file.txt', '2099-01-01T00:00:00Z')`,
		`INSERT INTO upload_sessions(id, account_id, space_id, mount_id, target_relative_path, declared_size, part_size, temp_dir, expires_at) VALUES ('stale-upload', ?, 'space-1', 'mount-1', 'file.txt', 1, 1, 'tmp', '2099-01-01T00:00:00Z')`,
		`INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, expires_at) VALUES ('stale-token', 'stale-token-public', 'hash', ?, 'Stale', 'files.list', '2099-01-01T00:00:00Z')`,
	}
	for _, query := range fixtures {
		if _, err := db.SQL().Exec(query, member.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SQL().Exec(`INSERT INTO ai_token_boundaries(token_id, space_id, mount_id, relative_path) VALUES ('stale-token', 'space-1', 'mount-1', '.')`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"permission": "editor"})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/mounts/mount-1/grants/"+member.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(issueAPITestSession(t, db, admin.ID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM mount_account_grants WHERE mount_id = 'mount-1' AND account_id = ?`, member.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale grant count = %d, want 0", count)
	}
	var activeShares, activeUploads, boundaries int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM shares WHERE id = 'stale-share' AND revoked_at IS NULL`).Scan(&activeShares); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM upload_sessions WHERE id = 'stale-upload' AND status = 'active'`).Scan(&activeUploads); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM ai_token_boundaries WHERE token_id = 'stale-token'`).Scan(&boundaries); err != nil {
		t.Fatal(err)
	}
	if activeShares != 0 || activeUploads != 0 || boundaries != 0 {
		t.Fatalf("dependent capabilities remain: shares=%d uploads=%d boundaries=%d", activeShares, activeUploads, boundaries)
	}
}

func TestCapabilityCreationRechecksAuthorizationAfterInsert(t *testing.T) {
	t.Run("share", func(t *testing.T) {
		db, handler := newShareAPITestServer(t)
		admin, _ := createAPITestAccounts(t, db)
		root := createTestSpaceAndMount(t, db, "space-race", "mount-race", admin.ID, "read_write")
		if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("report"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().Exec(`
CREATE TRIGGER revoke_share_grant_during_insert
BEFORE INSERT ON shares
BEGIN
  UPDATE mount_account_grants SET permission = 'viewer'
  WHERE mount_id = NEW.mount_id AND account_id = NEW.creator_account_id;
END;
`); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader([]byte(`{"spaceId":"space-race","mountId":"mount-race","relativePath":"report.txt"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(issueAPITestSession(t, db, admin.ID))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var active int
		if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM shares WHERE revoked_at IS NULL`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 0 {
			t.Fatalf("active shares = %d, want 0", active)
		}
	})

	t.Run("upload", func(t *testing.T) {
		db, handler := newAPITestServer(t)
		admin, _ := createAPITestAccounts(t, db)
		createTestSpaceAndMount(t, db, "space-race", "mount-race", admin.ID, "read_write")
		if _, err := db.SQL().Exec(`
CREATE TRIGGER revoke_upload_grant_during_insert
BEFORE INSERT ON upload_sessions
BEGIN
  DELETE FROM mount_account_grants
  WHERE mount_id = NEW.mount_id AND account_id = NEW.account_id;
END;
`); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewReader([]byte(`{"spaceId":"space-race","mountId":"mount-race","parentPath":".","fileName":"upload.txt","size":1}`)))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(issueAPITestSession(t, db, admin.ID))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var active int
		if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM upload_sessions WHERE status = 'active'`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 0 {
			t.Fatalf("active uploads = %d, want 0", active)
		}
	})

	t.Run("token", func(t *testing.T) {
		db, handler := newAPITestServer(t)
		admin, _ := createAPITestAccounts(t, db)
		createTestSpaceAndMount(t, db, "space-race", "mount-race", admin.ID, "read_only")
		if _, err := db.SQL().Exec(`
CREATE TRIGGER revoke_token_grant_during_insert
BEFORE INSERT ON ai_tokens
BEGIN
  DELETE FROM mount_account_grants WHERE account_id = NEW.account_id;
END;
`); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-tokens", bytes.NewReader([]byte(`{"name":"Race token","scopes":["files.list"],"boundaries":[{"spaceId":"space-race","mountId":"mount-race","relativePath":"."}],"expiresAt":"2099-01-01T00:00:00Z"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(issueAPITestSession(t, db, admin.ID))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var active, boundaries int
		if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM ai_tokens WHERE revoked_at IS NULL`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM ai_token_boundaries`).Scan(&boundaries); err != nil {
			t.Fatal(err)
		}
		if active != 0 || boundaries != 0 {
			t.Fatalf("active tokens = %d, boundaries = %d", active, boundaries)
		}
	})
}

func assertMountListCount(t *testing.T, handler http.Handler, cookie *http.Cookie, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces/space-1/mounts", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list mounts status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []mountDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != want {
		t.Fatalf("mount count = %d, want %d", len(payload.Items), want)
	}
}

func assertSessionContentAccess(t *testing.T, handler http.Handler, cookie *http.Cookie, want bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Capabilities["hasContentAccess"] != want {
		t.Fatalf("capabilities = %#v, want hasContentAccess=%t", payload.Capabilities, want)
	}
}
