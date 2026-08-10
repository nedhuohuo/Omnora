package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func TestMemberPersonalContentSourcesAndChildren(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "personal-api.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	memberID := "acct_member_visible"
	adminID := "acct_admin_hidden"
	for _, account := range []struct{ id, email, role string }{
		{memberID, "member@example.com", "member"},
		{adminID, "admin@example.com", "admin"},
	} {
		if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status, password_hash)
VALUES (?, ?, ?, ?, 'active', 'unused')
`, account.id, account.email, account.id, account.role); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().ExecContext(ctx, `INSERT INTO personal_directories(account_id, relative_path, state) VALUES (?, ?, 'ready')`, account.id, account.id); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(managed, "personal", account.id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(managed, "personal", memberID, "mine.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "personal", adminID, "admin.txt"), []byte("admin"), 0o600); err != nil {
		t.Fatal(err)
	}
	commonRoot, err := os.MkdirTemp(".", ".personal-common-")
	if err != nil {
		t.Fatal(err)
	}
	commonRoot, err = filepath.Abs(commonRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(commonRoot) })
	if err := os.WriteFile(filepath.Join(commonRoot, "team.txt"), []byte("team"), 0o600); err != nil {
		t.Fatal(err)
	}
	commonIdentity, err := mountid.Capture(commonRoot)
	if err != nil {
		t.Fatal(err)
	}
	commonIdentityJSON, err := json.Marshal(commonIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json)
VALUES ('common-visible', 'Team NAS', ?, 'common', 'external', 'normal', 'read_only', 'active', ?)
`, commonRoot, string(commonIdentityJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('common-visible', ?, 'viewer')`, memberID); err != nil {
		t.Fatal(err)
	}
	issued, err := identity.New(db.SQL(), identity.Options{}).CreateSession(ctx, identity.SessionRequest{
		AccountID: memberID, TTL: time.Hour, Entry: EntryHTTP, Purpose: identity.SessionPurposeFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Storage:           config.StorageConfig{ManagedDir: managed},
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}
	handler := New(cfg, db)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member/content-sources", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("content sources status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sources struct {
		Personal struct {
			Source string `json:"source"`
		} `json:"personal"`
		CommonMounts []struct {
			MountID    string `json:"mountId"`
			Permission string `json:"permission"`
		} `json:"commonMounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sources); err != nil {
		t.Fatal(err)
	}
	if sources.Personal.Source != "personal" || len(sources.CommonMounts) != 1 || sources.CommonMounts[0].MountID != "common-visible" || sources.CommonMounts[0].Permission != "viewer" {
		t.Fatalf("content sources = %#v", sources)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/member/files/children?source=personal&path=.&accountId="+adminID, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forbidden accountId status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/member/files/children?source=personal&path=.", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("personal children status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "mine.txt" {
		t.Fatalf("entries = %#v", listing.Entries)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/member/files/children?source=common_mount&mountId=common-visible&path=.", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("common children status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "team.txt" {
		t.Fatalf("common entries = %#v", listing.Entries)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/member/files/directories", bytes.NewBufferString(`{"source":"personal","path":".","name":"docs"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create personal directory status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/member/files/download?source=personal&path=mine.txt", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "mine" {
		t.Fatalf("personal download status/body = %d/%q", rec.Code, rec.Body.String())
	}

	if _, err := db.SQL().Exec(`DELETE FROM mount_grants WHERE mount_id = 'common-visible' AND account_id = ?`, memberID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/member/files/children?source=common_mount&mountId=common-visible&path=.", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ungranted common mount status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestMemberCollaborationsListsEmptyTargetModel(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "collaborations-api.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := identity.New(db.SQL(), identity.Options{ManagedDir: managed}).CreateAccount(ctx, identity.CreateAccountRequest{
		Email: "member-collaboration@example.com", DisplayName: "Member", Password: "CorrectHorse1!", Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := identity.New(db.SQL(), identity.Options{}).CreateSession(ctx, identity.SessionRequest{
		AccountID: created.Account.ID, TTL: time.Hour, Entry: EntryHTTP, Purpose: identity.SessionPurposeFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(config.Config{
		Storage: config.StorageConfig{ManagedDir: managed}, Routes: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member/collaborations?direction=incoming", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("collaborations status=%d body=%s", rec.Code, rec.Body.String())
	}
}
