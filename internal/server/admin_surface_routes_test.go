package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKnownAdminSurfacesDoNotFallThroughToProductPlaceholder(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)

	for _, path := range []string{
		"/api/v1/admin/mounts",
		"/api/v1/admin/shares",
		"/api/v1/audit/events?limit=10",
	} {
		t.Run(path, func(t *testing.T) {
			rec := authorizedAPITestRequest(t, handler, path, adminCookie)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d, body = %s", path, rec.Code, http.StatusOK, rec.Body.String())
			}
		})
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/shares/missing-share", nil)
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE admin share status = %d, want %d, body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestAdminShareGovernanceDoesNotExposeFragmentSecret(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	createTestMount(t, db, "admin-share-mount", admin.ID, "read_write")
	if _, err := db.SQL().Exec(`
INSERT INTO shares(id, public_id, secret_hash, creator_account_id, mount_id, relative_path, expires_at, fragment_secret)
VALUES ('share-admin-visible', 'public-for-admin', 'hashed-secret', ?, 'admin-share-mount', 'docs/readme.txt', ?, 'secret-for-admin')
`, admin.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert share: %v", err)
	}

	rec := authorizedAPITestRequest(t, handler, "/api/v1/admin/shares", issueAPITestSession(t, db, admin.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET admin shares status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-for-admin") || strings.Contains(rec.Body.String(), `"fragment"`) {
		t.Fatalf("admin share governance exposed a fragment secret: %s", rec.Body.String())
	}
}
