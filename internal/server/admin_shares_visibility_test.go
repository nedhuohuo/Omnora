package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omnora/internal/store"
)

func TestAdminShareGovernanceFiltersRestrictedMountsByAdministrator(t *testing.T) {
	db, handler := newAPITestServer(t)
	initialAdmin, ordinaryAdmin := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`UPDATE accounts SET role = 'admin' WHERE id = ?`, ordinaryAdmin.ID); err != nil {
		t.Fatalf("promote ordinary administrator: %v", err)
	}

	createTestMount(t, db, "normal-admin-share-mount", initialAdmin.ID, "read_write")
	createRestrictedTestMount(t, db, "restricted-admin-share-mount", initialAdmin.ID)
	insertAdminShareFixture(t, db.SQL(), "normal-admin-share", "normal-admin-share-mount", initialAdmin.ID)
	insertAdminShareFixture(t, db.SQL(), "restricted-admin-share", "restricted-admin-share-mount", initialAdmin.ID)

	ordinaryResponse := authorizedAPITestRequest(t, handler, "/api/v1/admin/shares", issueAPITestSession(t, db, ordinaryAdmin.ID))
	if ordinaryResponse.Code != http.StatusOK {
		t.Fatalf("ordinary admin GET shares status = %d, body = %s", ordinaryResponse.Code, ordinaryResponse.Body.String())
	}
	if !strings.Contains(ordinaryResponse.Body.String(), `"id":"normal-admin-share"`) {
		t.Fatalf("ordinary admin did not see normal-mount share: %s", ordinaryResponse.Body.String())
	}
	if strings.Contains(ordinaryResponse.Body.String(), `"id":"restricted-admin-share"`) {
		t.Fatalf("ordinary admin saw restricted-mount share: %s", ordinaryResponse.Body.String())
	}

	initialResponse := authorizedAPITestRequest(t, handler, "/api/v1/admin/shares", issueAPITestSession(t, db, initialAdmin.ID))
	if initialResponse.Code != http.StatusOK {
		t.Fatalf("initial admin GET shares status = %d, body = %s", initialResponse.Code, initialResponse.Body.String())
	}
	for _, shareID := range []string{"normal-admin-share", "restricted-admin-share"} {
		if !strings.Contains(initialResponse.Body.String(), `"id":"`+shareID+`"`) {
			t.Fatalf("initial admin did not see %s: %s", shareID, initialResponse.Body.String())
		}
	}
}

func TestAdminShareRevokeAppliesAdministratorMountVisibilityAndAuditsSuccess(t *testing.T) {
	db, handler := newAPITestServer(t)
	initialAdmin, ordinaryAdmin := createAPITestAccounts(t, db)
	if _, err := db.SQL().Exec(`UPDATE accounts SET role = 'admin' WHERE id = ?`, ordinaryAdmin.ID); err != nil {
		t.Fatalf("promote ordinary administrator: %v", err)
	}

	createTestMount(t, db, "normal-revoke-mount", initialAdmin.ID, "read_write")
	createRestrictedTestMount(t, db, "restricted-revoke-mount", initialAdmin.ID)
	insertAdminShareFixture(t, db.SQL(), "normal-revoke-share", "normal-revoke-mount", initialAdmin.ID)
	insertAdminShareFixture(t, db.SQL(), "restricted-revoke-share", "restricted-revoke-mount", initialAdmin.ID)

	ordinaryCookie := issueAPITestSession(t, db, ordinaryAdmin.ID)
	hidden := revokeAdminShareRequest(handler, ordinaryCookie, "restricted-revoke-share")
	if hidden.Code != http.StatusNotFound {
		t.Fatalf("ordinary admin DELETE restricted share status = %d, want %d, body = %s", hidden.Code, http.StatusNotFound, hidden.Body.String())
	}
	assertShareNotRevoked(t, db.SQL(), "restricted-revoke-share")
	assertAdminShareRevokeAuditCount(t, db.SQL(), "restricted-revoke-share", 0)

	normal := revokeAdminShareRequest(handler, ordinaryCookie, "normal-revoke-share")
	if normal.Code != http.StatusNoContent {
		t.Fatalf("ordinary admin DELETE normal share status = %d, want %d, body = %s", normal.Code, http.StatusNoContent, normal.Body.String())
	}
	assertShareRevoked(t, db.SQL(), "normal-revoke-share")
	assertAdminShareRevokeAuditCount(t, db.SQL(), "normal-revoke-share", 1)

	initial := revokeAdminShareRequest(handler, issueAPITestSession(t, db, initialAdmin.ID), "restricted-revoke-share")
	if initial.Code != http.StatusNoContent {
		t.Fatalf("initial admin DELETE restricted share status = %d, want %d, body = %s", initial.Code, http.StatusNoContent, initial.Body.String())
	}
	assertShareRevoked(t, db.SQL(), "restricted-revoke-share")
	assertAdminShareRevokeAuditCount(t, db.SQL(), "restricted-revoke-share", 1)
}

func insertAdminShareFixture(t *testing.T, db *sql.DB, shareID, mountID, creatorAccountID string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO shares(id, public_id, secret_hash, creator_account_id, mount_id, relative_path, expires_at, fragment_secret)
VALUES (?, ?, 'hashed-secret', ?, ?, 'docs/readme.txt', ?, 'fixture-fragment-secret')
`, shareID, "public-"+shareID, creatorAccountID, mountID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert share %s: %v", shareID, err)
	}
}

func createRestrictedTestMount(t *testing.T, db *store.DB, mountID, ownerAccountID string) {
	t.Helper()
	createTestMount(t, db, mountID, ownerAccountID, "read_write")
	sqlDB := db.SQL()
	var displayName, rootPath, identityJSON string
	if err := sqlDB.QueryRow(`
SELECT display_name, root_path, mount_identity_json
FROM mounts
WHERE id = ?
`, mountID).Scan(&displayName, &rootPath, &identityJSON); err != nil {
		t.Fatalf("load test mount %s: %v", mountID, err)
	}
	if _, err := sqlDB.Exec(`DELETE FROM mounts WHERE id = ?`, mountID); err != nil {
		t.Fatalf("replace test mount %s: %v", mountID, err)
	}
	if _, err := sqlDB.Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, ?, 'common', 'external', 'restricted', 'read_write', 0, 'active', ?)
`, mountID, displayName, rootPath, identityJSON); err != nil {
		t.Fatalf("insert restricted test mount %s: %v", mountID, err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO mount_grants(mount_id, account_id, permission) VALUES (?, ?, 'editor')`, mountID, ownerAccountID); err != nil {
		t.Fatalf("grant restricted test mount %s: %v", mountID, err)
	}
}

func revokeAdminShareRequest(handler http.Handler, cookie *http.Cookie, shareID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/shares/"+shareID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertShareNotRevoked(t *testing.T, db *sql.DB, shareID string) {
	t.Helper()
	var revokedAt sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM shares WHERE id = ?`, shareID).Scan(&revokedAt); err != nil {
		t.Fatalf("load share %s: %v", shareID, err)
	}
	if revokedAt.Valid {
		t.Fatalf("share %s was unexpectedly revoked at %q", shareID, revokedAt.String)
	}
}

func assertShareRevoked(t *testing.T, db *sql.DB, shareID string) {
	t.Helper()
	var revokedAt sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM shares WHERE id = ?`, shareID).Scan(&revokedAt); err != nil {
		t.Fatalf("load share %s: %v", shareID, err)
	}
	if !revokedAt.Valid || revokedAt.String == "" {
		t.Fatalf("share %s was not revoked", shareID)
	}
}

func assertAdminShareRevokeAuditCount(t *testing.T, db *sql.DB, shareID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
SELECT COUNT(1)
FROM audit_events
WHERE action = 'admin_share_revoke' AND target_type = 'share' AND target_id = ?
`, shareID).Scan(&got); err != nil {
		t.Fatalf("count admin share audit for %s: %v", shareID, err)
	}
	if got != want {
		t.Fatalf("admin share audit count for %s = %d, want %d", shareID, got, want)
	}
}
