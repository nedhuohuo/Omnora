package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/foldercollab"
	"omnora/internal/identity"
	"omnora/internal/store"
)

func TestMemberCollaborationDownloadUsesCollaborationLocator(t *testing.T) {
	ctx := context.Background()
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "collaboration-download.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	identities := identity.New(db.SQL(), identity.Options{ManagedDir: managed})
	owner, err := identities.CreateAccount(ctx, identity.CreateAccountRequest{
		Email: "collaboration-owner@example.com", DisplayName: "Owner", Password: "CorrectHorse1!", Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := identities.CreateAccount(ctx, identity.CreateAccountRequest{
		Email: "collaboration-recipient@example.com", DisplayName: "Recipient", Password: "CorrectHorse1!", Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := identities.CreateAccount(ctx, identity.CreateAccountRequest{
		Email: "collaboration-outsider@example.com", DisplayName: "Outsider", Password: "CorrectHorse1!", Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}

	sharedRoot := filepath.Join(managed, "personal", owner.Account.ID, "shared")
	if err := os.Mkdir(sharedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedRoot, "brief.txt"), []byte("collaboration brief"), 0o600); err != nil {
		t.Fatal(err)
	}
	grant, err := foldercollab.New(db.SQL(), managed).Create(ctx, owner.Account.ID, foldercollab.CreateRequest{
		RecipientID: recipient.Account.ID, RootRelativePath: "shared", Permission: domain.ContentPermissionEditor,
	})
	if err != nil {
		t.Fatal(err)
	}

	issueSession := func(accountID string) string {
		t.Helper()
		issued, err := identities.CreateSession(ctx, identity.SessionRequest{
			AccountID: accountID, TTL: time.Hour, Entry: EntryHTTP, Purpose: identity.SessionPurposeFull,
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	handler := New(config.Config{
		Storage: config.StorageConfig{ManagedDir: managed},
		Routes:  map[domain.RouteGroup]bool{domain.RouteGroupREST: true}, RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member/files/download?source=collaboration&collaborationId="+grant.ID+"&path=brief.txt", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issueSession(recipient.Account.ID)})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "collaboration brief" {
		t.Fatalf("authorized collaboration download status/body = %d/%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/member/files/download?source=collaboration&collaborationId="+grant.ID+"&path=brief.txt", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issueSession(outsider.Account.ID)})
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("unguarded collaboration download unexpectedly succeeded: %q", rec.Body.String())
	}
}

func TestMemberCollaborationQueryLocatorRejectsMountAndLegacySpaceFields(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "missing collaboration ID", url: "/?source=collaboration&path=brief.txt"},
		{name: "mount ID", url: "/?source=collaboration&collaborationId=collab-1&mountId=mount-1&path=brief.txt"},
		{name: "legacy Space ID", url: "/?source=collaboration&collaborationId=collab-1&spaceId=space-1&path=brief.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := memberLocatorFromQuery(httptest.NewRequest(http.MethodGet, tt.url, nil)); err == nil {
				t.Fatal("memberLocatorFromQuery() error = nil")
			}
		})
	}
}
