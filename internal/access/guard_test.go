package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

type guardFixture struct {
	db       *sql.DB
	root     string
	spaceID  string
	mountID  string
	account  string
	identity string
}

func newGuardFixture(t *testing.T) guardFixture {
	t.Helper()
	dbHandle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "guard.db"),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = dbHandle.Close() })
	db := dbHandle.SQL()
	root, err := os.MkdirTemp(".", ".guard-mount-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("absolute mount root: %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture mount identity: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal mount identity: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-manager', 'manager@example.test', 'Manager', 'member', 'active'),
       ('acct-editor', 'editor@example.test', 'Editor', 'member', 'active'),
       ('acct-viewer', 'viewer@example.test', 'Viewer', 'member', 'active')
`); err != nil {
		t.Fatalf("insert accounts: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-1', 'shared', 'Files', 'acct-manager', 'active')
`); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-1', 'acct-manager', 'manager'),
       ('space-1', 'acct-editor', 'editor'),
       ('space-1', 'acct-viewer', 'viewer')
`); err != nil {
		t.Fatalf("insert memberships: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status, mount_identity_json)
VALUES ('mount-1', 'space-1', 'Files', ?, 'managed', 'read_write', 'active', ?)
`, root, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	return guardFixture{db: db, root: root, spaceID: "space-1", mountID: "mount-1", account: "acct-manager", identity: string(identityJSON)}
}

func issueGuardToken(t *testing.T, db *sql.DB, accountID string, scopes []aitoken.Scope, boundary string) aitoken.Principal {
	t.Helper()
	issued, err := aitoken.NewService(db).Create(context.Background(), aitoken.CreateRequest{
		AccountID: accountID,
		Name:      "guard test",
		Scopes:    scopes,
		Boundaries: []aitoken.DirectoryBoundary{{
			SpaceID:      "space-1",
			MountID:      "mount-1",
			RelativePath: boundary,
		}},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return aitoken.Principal{
		AccountID:  issued.Token.AccountID,
		TokenID:    issued.Token.ID,
		PublicID:   issued.Token.PublicID,
		Scopes:     issued.Token.Scopes,
		Boundaries: issued.Token.Boundaries,
		ExpiresAt:  issued.Token.ExpiresAt,
	}
}

func browserRequest(accountID, scope, path string, write bool, permission domain.SpacePermission) CheckRequest {
	return CheckRequest{
		Subject:            Subject{AccountID: accountID},
		Scope:              aitoken.Scope(scope),
		Locator:            Locator{SpaceID: "space-1", MountID: "mount-1", Path: path},
		RequiredPermission: permission,
		Write:              write,
	}
}

func TestGuardAuthorizationMatrix(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	ctx := context.Background()

	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), "docs", false, domain.SpacePermissionViewer)); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), "docs", true, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("viewer write unexpectedly authorized")
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-editor", string(aitoken.ScopeFilesWrite), "docs", true, domain.SpacePermissionEditor)); err != nil {
		t.Fatalf("editor write: %v", err)
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-manager", string(aitoken.ScopeSharesCreate), "docs", false, domain.SpacePermissionManager)); err != nil {
		t.Fatalf("manager operation: %v", err)
	}

	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), "docs", false, domain.SpacePermissionManager)); err == nil {
		t.Fatal("viewer manager operation unexpectedly authorized")
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), "/docs", false, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("absolute path unexpectedly authorized")
	}
	for _, invalid := range []string{"../docs", "docs/../../secret", ".omnora", ".omnora/index"} {
		if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), invalid, false, domain.SpacePermissionViewer)); err == nil {
			t.Fatalf("path %q unexpectedly authorized", invalid)
		}
	}

	if _, err := f.db.Exec(`UPDATE accounts SET status = 'disabled' WHERE id = 'acct-viewer'`); err != nil {
		t.Fatalf("disable account: %v", err)
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), ".", false, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("disabled account unexpectedly authorized")
	}
	if _, err := f.db.Exec(`UPDATE accounts SET status = 'active' WHERE id = 'acct-viewer'`); err != nil {
		t.Fatalf("restore account: %v", err)
	}
	if _, err := f.db.Exec(`UPDATE spaces SET status = 'disabled' WHERE id = 'space-1'`); err != nil {
		t.Fatalf("disable space: %v", err)
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), ".", false, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("disabled space unexpectedly authorized")
	}
	if _, err := f.db.Exec(`UPDATE spaces SET status = 'active' WHERE id = 'space-1'`); err != nil {
		t.Fatalf("restore space: %v", err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET status = 'disabled' WHERE id = 'mount-1'`); err != nil {
		t.Fatalf("disable mount: %v", err)
	}
	if _, err := guard.Authorize(ctx, browserRequest("acct-viewer", string(aitoken.ScopeFilesList), ".", false, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("disabled mount unexpectedly authorized")
	}
}

func TestGuardTokenRefreshBoundariesAndPair(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	allScopes := aitoken.AllowlistedScopes()
	principal := issueGuardToken(t, f.db, "acct-viewer", allScopes, ".")
	req := CheckRequest{
		Subject:            Subject{AccountID: "acct-viewer", Principal: &principal},
		Scope:              aitoken.ScopeFilesList,
		Locator:            Locator{SpaceID: f.spaceID, MountID: f.mountID, Path: "docs"},
		RequiredPermission: domain.SpacePermissionViewer,
	}
	if _, err := guard.Authorize(context.Background(), req); err != nil {
		t.Fatalf("token authorization: %v", err)
	}
	if _, err := f.db.Exec(`UPDATE ai_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, principal.TokenID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := guard.Authorize(context.Background(), req); err == nil {
		t.Fatal("revoked token unexpectedly authorized after refresh")
	}

	principal = issueGuardToken(t, f.db, "acct-viewer", []aitoken.Scope{aitoken.ScopeFilesList}, "docs")
	req.Subject.Principal = &principal
	if _, err := guard.Authorize(context.Background(), req); err != nil {
		t.Fatalf("boundary root child: %v", err)
	}
	req.Locator.Path = "docs-private/report.txt"
	if _, err := guard.Authorize(context.Background(), req); err == nil {
		t.Fatal("adjacent boundary unexpectedly authorized")
	}
	req.Scope = aitoken.ScopeFilesText
	req.Locator.Path = "docs/report.txt"
	if _, err := guard.Authorize(context.Background(), req); err == nil {
		t.Fatal("missing scope unexpectedly authorized")
	}

	principal = issueGuardToken(t, f.db, "acct-viewer", []aitoken.Scope{aitoken.ScopeFilesList}, ".")
	req.Scope = aitoken.ScopeFilesList
	validSource := req
	validSource.Subject.Principal = &principal
	validSource.Locator.Path = "source.txt"
	validDestination := validSource
	validDestination.Locator.Path = "dest.txt"
	if pair, err := guard.AuthorizePair(context.Background(), validSource, validDestination); err != nil || pair.Source.RelativePath != "source.txt" || pair.Destination.RelativePath != "dest.txt" {
		t.Fatalf("valid copy/move pair = %#v, err=%v", pair, err)
	}
	validDestination.Locator.Path = "../outside"
	if _, err := guard.AuthorizePair(context.Background(), validSource, validDestination); err == nil {
		t.Fatal("invalid destination pair unexpectedly authorized")
	}
}

func TestGuardRejectsSymlinkAndMountIdentityDrift(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	if err := os.Symlink(f.root, filepath.Join(f.root, "alias")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := guard.Authorize(context.Background(), browserRequest("acct-viewer", string(aitoken.ScopeFilesList), "alias", false, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("symlink path unexpectedly authorized")
	}
	otherRoot, err := os.MkdirTemp(".", ".guard-drift-")
	if err != nil {
		t.Fatalf("create drift root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(otherRoot) })
	otherRoot, err = filepath.Abs(otherRoot)
	if err != nil {
		t.Fatalf("absolute drift root: %v", err)
	}
	otherIdentity, err := mountid.Capture(otherRoot)
	if err != nil {
		t.Fatalf("capture drift identity: %v", err)
	}
	otherJSON, _ := json.Marshal(otherIdentity)
	if _, err := f.db.Exec(`UPDATE mounts SET mount_identity_json = ? WHERE id = ?`, string(otherJSON), f.mountID); err != nil {
		t.Fatalf("set drift identity: %v", err)
	}
	if _, err := guard.Authorize(context.Background(), browserRequest("acct-viewer", string(aitoken.ScopeFilesList), ".", false, domain.SpacePermissionViewer)); err == nil {
		t.Fatal("mount identity drift unexpectedly authorized")
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM mounts WHERE id = ?`, f.mountID).Scan(&status); err != nil {
		t.Fatalf("read mount status: %v", err)
	}
	if status != "unavailable" {
		t.Fatalf("mount status = %q, want unavailable", status)
	}
}
