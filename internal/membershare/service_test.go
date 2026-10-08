package membershare

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/share"
	"omnora/internal/store"
)

type shareFixture struct {
	db      *sql.DB
	root    string
	spaceID string
	mountID string
	now     time.Time
}

func newShareFixture(t *testing.T) shareFixture {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "membershare.db")})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	root, err := os.MkdirTemp(".", ".membershare-mount-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs(root) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "readme.txt"), []byte("member share"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal(identity) error = %v", err)
	}
	_, err = handle.SQL().Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-manager', 'manager@example.test', 'Manager', 'member', 'active'),
       ('acct-viewer', 'viewer@example.test', 'Viewer', 'member', 'active');
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-share', 'shared', 'Share Space', 'acct-manager', 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-share', 'acct-manager', 'manager'),
       ('space-share', 'acct-viewer', 'viewer');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status, mount_identity_json)
VALUES ('mount-share', 'space-share', 'Files', ?, 'managed', 'read_write', 'active', ?)
`, root, string(identityJSON))
	if err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	return shareFixture{db: handle.SQL(), root: root, spaceID: "space-share", mountID: "mount-share", now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
}

func (f shareFixture) service() *Service {
	return NewService(f.db, access.NewGuard(f.db), WithClock(func() time.Time { return f.now }))
}

func (f shareFixture) manager() access.Subject { return access.Subject{AccountID: "acct-manager"} }

func (f shareFixture) viewer() access.Subject { return access.Subject{AccountID: "acct-viewer"} }

func (f shareFixture) locator(path string) access.Locator {
	return access.Locator{SpaceID: f.spaceID, MountID: f.mountID, Path: path}
}

func (f shareFixture) spaceGrantToken(t *testing.T, permission domain.SpacePermission, denies []aitoken.FolderDeny) access.Subject {
	t.Helper()
	issued, err := aitoken.NewService(f.db).CreateSpaceGrant(context.Background(), aitoken.CreateSpaceGrantRequest{
		AccountID: "acct-manager",
		Name:      "membershare space grant token",
		Scopes:    []aitoken.Scope{aitoken.ScopeSharesCreate, aitoken.ScopeSharesRead, aitoken.ScopeSharesRevoke},
		SpaceGrants: []aitoken.SpaceGrant{{
			SpaceID: f.spaceID, Permission: permission, FolderDenylist: denies,
		}},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSpaceGrant(token) error = %v", err)
	}
	principal := aitoken.Principal{
		AccountID:          issued.Token.AccountID,
		TokenID:            issued.Token.ID,
		PublicID:           issued.Token.PublicID,
		Scopes:             issued.Token.Scopes,
		AuthorizationModel: issued.Token.AuthorizationModel,
		SpaceGrants:        issued.Token.SpaceGrants,
		ExpiresAt:          issued.Token.ExpiresAt,
	}
	return access.Subject{AccountID: principal.AccountID, Principal: &principal}
}

func shareDirectoryFingerprint(t *testing.T, target string) string {
	t.Helper()
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", target, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		t.Fatal("directory stat did not expose syscall.Stat_t")
	}
	return fmt.Sprintf("dev:%d:ino:%d", uint64(stat.Dev), uint64(stat.Ino))
}

func TestCreateIsManagerOnlyValidatesOptionsAndReturnsOneTimeCapability(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	ctx := context.Background()
	issued, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.Secret == "" || issued.Fragment == "" || issued.URL != "/share#"+issued.Fragment {
		t.Fatalf("issued = %#v, want one-time fragment and URL", issued)
	}
	if !strings.HasPrefix(issued.Fragment, issued.PublicID+".") {
		t.Fatalf("fragment = %q, want public-id prefix", issued.Fragment)
	}
	if issued.AllowPreview != true || issued.AllowDownload != true {
		t.Fatalf("default capabilities = preview=%v download=%v", issued.AllowPreview, issued.AllowDownload)
	}
	if _, err := s.Create(ctx, f.viewer(), CreateRequest{Locator: f.locator("docs/readme.txt")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer Create() error = %v, want forbidden", err)
	}
	falseValue := false
	if _, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt"), AllowPreview: &falseValue, AllowDownload: &falseValue}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("no-capability Create() error = %v, want invalid input", err)
	}
	if _, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt"), MaxVisits: ptrInt64(0)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero max visits error = %v, want invalid input", err)
	}
	if _, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt"), MaxDownloads: ptrInt64(1), AllowDownload: &falseValue}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("download limit without download error = %v, want invalid input", err)
	}
	if _, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/../readme.txt")}); !errors.Is(err, access.ErrBoundaryViolation) && !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("traversal Create() error = %v, want path rejection", err)
	}
	if _, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt"), ExpiresAt: f.now}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expired Create() error = %v, want invalid input", err)
	}

	var storedSecret, fragmentSecret string
	if err := f.db.QueryRow(`SELECT secret_hash, COALESCE(fragment_secret, '') FROM shares WHERE id = ?`, issued.ID).Scan(&storedSecret, &fragmentSecret); err != nil {
		t.Fatalf("query stored secret: %v", err)
	}
	if storedSecret == issued.Secret || fragmentSecret != "" || !share.VerifySecret(issued.Secret, storedSecret) {
		t.Fatalf("share secret persistence is unsafe: hash=%q fragment=%q", storedSecret, fragmentSecret)
	}
}

func TestListVisibilityNeverReturnsExistingFragmentOrPasswordMaterial(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	issued, err := s.Create(context.Background(), f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt"), Password: "correct horse"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	items, err := s.List(context.Background(), f.manager(), ListFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("List() = %#v, error = %v", items, err)
	}
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("Marshal(list) error = %v", err)
	}
	if strings.Contains(string(payload), issued.Secret) || strings.Contains(string(payload), issued.Fragment) || strings.Contains(string(payload), "password") {
		t.Fatalf("list leaked secret material: %s", payload)
	}
	if _, err := s.List(context.Background(), f.viewer(), ListFilter{}); !errors.Is(err, ErrForbidden) && err != nil {
		// A viewer with shares:read is still allowed to call the service, but
		// should receive no shares. This branch documents the stable choice.
		t.Fatalf("viewer List() error = %v", err)
	}
	viewerItems, err := s.List(context.Background(), f.viewer(), ListFilter{})
	if err != nil {
		t.Fatalf("viewer List() error = %v", err)
	}
	if len(viewerItems) != 0 {
		t.Fatalf("viewer List() = %#v, want no visible shares", viewerItems)
	}
}

func TestListUsesCreatorOrCurrentManagerWithLiveMountChecks(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	ctx := context.Background()
	owned, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt")})
	if err != nil {
		t.Fatalf("Create(owned) error = %v", err)
	}
	// A creator remains able to see their own share even after ACL removal.
	if _, err := f.db.Exec(`UPDATE space_members SET permission = 'viewer' WHERE space_id = ? AND account_id = ?`, f.spaceID, "acct-manager"); err != nil {
		t.Fatalf("demote creator ACL: %v", err)
	}
	items, err := s.List(ctx, f.manager(), ListFilter{})
	if err != nil || len(items) != 1 || items[0].ID != owned.ID {
		t.Fatalf("List(creator without ACL) = %#v, error = %v", items, err)
	}

	// Restore manager access and insert a share created by another member so
	// the current-manager branch is exercised independently.
	if _, err := f.db.Exec(`UPDATE space_members SET permission = 'manager' WHERE space_id = ? AND account_id = ?`, f.spaceID, "acct-manager"); err != nil {
		t.Fatalf("restore manager ACL: %v", err)
	}
	if _, err := f.db.Exec(`
INSERT INTO shares(id, public_id, secret_hash, creator_account_id, space_id, mount_id, relative_path, expires_at)
VALUES ('share-viewer', 'pub-viewer', ?, 'acct-viewer', ?, ?, 'docs/readme.txt', ?)
`, share.HashSecret("viewer-secret"), f.spaceID, f.mountID, formatSQLiteTime(f.now.Add(time.Hour))); err != nil {
		t.Fatalf("insert viewer share: %v", err)
	}
	items, err = s.List(ctx, f.manager(), ListFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("List(current manager) = %#v, error = %v", items, err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET status = 'disabled' WHERE id = ?`, f.mountID); err != nil {
		t.Fatalf("disable mount: %v", err)
	}
	if items, err := s.List(ctx, f.manager(), ListFilter{}); err != nil || len(items) != 0 {
		t.Fatalf("List(disabled mount) = %#v, error = %v, want empty", items, err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET status = 'active', mount_identity_json = '{}' WHERE id = ?`, f.mountID); err != nil {
		t.Fatalf("set drifted mount identity: %v", err)
	}
	if items, err := s.List(ctx, f.manager(), ListFilter{}); err != nil || len(items) != 0 {
		t.Fatalf("List(drifted mount identity) = %#v, error = %v, want empty", items, err)
	}
}

func TestRevokeAllowsCreatorOrCurrentManagerAndInvalidatesPathDescendants(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	ctx := context.Background()
	first, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt")})
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	second, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs")})
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	// The creator is allowed to revoke its own share.
	if err := s.Revoke(ctx, f.manager(), first.ID); err != nil {
		t.Fatalf("Revoke(creator) error = %v", err)
	}
	if _, err := share.NewService(f.db, share.WithClock(func() time.Time { return f.now })).Exchange(ctx, share.ExchangeRequest{PublicID: first.PublicID, FragmentSecret: first.Secret}); err == nil {
		t.Fatal("revoked share exchange unexpectedly succeeded")
	}
	third, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt")})
	if err != nil {
		t.Fatalf("Create(third) error = %v", err)
	}
	if err := s.Revoke(ctx, f.viewer(), third.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer Revoke() error = %v, want forbidden", err)
	}
	if err := s.RevokePath(ctx, f.spaceID, f.mountID, "docs"); err != nil {
		t.Fatalf("RevokePath() error = %v", err)
	}
	var revoked int
	if err := f.db.QueryRow(`SELECT COUNT(1) FROM shares WHERE space_id = ? AND revoked_at IS NOT NULL`, f.spaceID).Scan(&revoked); err != nil {
		t.Fatalf("count revoked: %v", err)
	}
	if revoked != 3 {
		t.Fatalf("revoked count = %d, want all shares under docs", revoked)
	}
	if err := s.RevokePath(ctx, f.spaceID, f.mountID, ".omnora/secret"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reserved RevokePath() error = %v, want invalid input", err)
	}
	_ = second
}

func TestTokenScopesAndBoundariesApplyToMemberShares(t *testing.T) {
	f := newShareFixture(t)
	issued, err := aitoken.NewService(f.db).Create(context.Background(), aitoken.CreateRequest{
		AccountID: "acct-manager", Name: "share token", Scopes: []aitoken.Scope{aitoken.ScopeSharesCreate, aitoken.ScopeSharesRead, aitoken.ScopeSharesRevoke},
		Boundaries: []aitoken.DirectoryBoundary{{SpaceID: f.spaceID, MountID: f.mountID, RelativePath: "docs"}}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create(token) error = %v", err)
	}
	principal := aitoken.Principal{AccountID: issued.Token.AccountID, TokenID: issued.Token.ID, PublicID: issued.Token.PublicID, Scopes: issued.Token.Scopes, Boundaries: issued.Token.Boundaries, ExpiresAt: issued.Token.ExpiresAt}
	subject := access.Subject{AccountID: principal.AccountID, Principal: &principal}
	s := f.service()
	if _, err := s.Create(context.Background(), subject, CreateRequest{Locator: f.locator("docs/readme.txt")}); err != nil {
		t.Fatalf("token Create() error = %v", err)
	}
	if _, err := s.Create(context.Background(), subject, CreateRequest{Locator: f.locator("outside.txt")}); !errors.Is(err, access.ErrBoundaryViolation) {
		t.Fatalf("token boundary Create() error = %v, want boundary violation", err)
	}
	if _, err := s.List(context.Background(), subject, ListFilter{}); err != nil {
		t.Fatalf("token List() error = %v", err)
	}
	if _, err := f.db.Exec(`UPDATE ai_tokens SET revoked_at = ? WHERE id = ?`, formatSQLiteTime(f.now), issued.Token.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := s.List(context.Background(), subject, ListFilter{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token List() error = %v, want unauthorized", err)
	}
}

func TestSpaceGrantSharesRecheckGrantDenyAndCreatorACL(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	ctx := context.Background()
	privateDir := filepath.Join(f.root, "private")
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(private) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(secret) error = %v", err)
	}
	allowed, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("docs/readme.txt")})
	if err != nil {
		t.Fatalf("Create(allowed session share) error = %v", err)
	}
	denied, err := s.Create(ctx, f.manager(), CreateRequest{Locator: f.locator("private/secret.txt")})
	if err != nil {
		t.Fatalf("Create(denied session share) error = %v", err)
	}
	subject := f.spaceGrantToken(t, domain.SpacePermissionEditor, []aitoken.FolderDeny{{
		MountID:             f.mountID,
		RelativePath:        "private",
		IdentityFingerprint: shareDirectoryFingerprint(t, privateDir),
	}})

	created, err := s.Create(ctx, subject, CreateRequest{Locator: f.locator("docs/readme.txt")})
	if err != nil {
		t.Fatalf("Create(space grant editor) error = %v", err)
	}
	if _, err := s.Create(ctx, subject, CreateRequest{Locator: f.locator("private/secret.txt")}); !errors.Is(err, access.ErrFolderDenied) {
		t.Fatalf("Create(denied path) error = %v, want folder denied", err)
	}
	items, err := s.List(ctx, subject, ListFilter{})
	if err != nil {
		t.Fatalf("List(space grant) error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List(space grant) = %#v, want two allowed shares", items)
	}
	for _, item := range items {
		if item.ID == denied.ID || strings.Contains(item.RelativePath, "private") {
			t.Fatalf("List(space grant) leaked denied share: %#v", item)
		}
	}
	if err := s.Revoke(ctx, subject, denied.ID); !errors.Is(err, access.ErrFolderDenied) {
		t.Fatalf("Revoke(denied creator share) error = %v, want folder denied", err)
	}
	viewerSubject := f.spaceGrantToken(t, domain.SpacePermissionViewer, nil)
	if _, err := s.Create(ctx, viewerSubject, CreateRequest{Locator: f.locator("docs/readme.txt")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Create(space grant viewer) error = %v, want forbidden", err)
	}
	if err := s.Revoke(ctx, viewerSubject, allowed.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Revoke(space grant viewer) error = %v, want forbidden", err)
	}

	if _, err := f.db.Exec(`DELETE FROM space_members WHERE space_id = ? AND account_id = ?`, f.spaceID, subject.AccountID); err != nil {
		t.Fatalf("remove creator ACL: %v", err)
	}
	if err := s.Revoke(ctx, subject, allowed.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Revoke(creator after ACL removal) error = %v, want forbidden", err)
	}
	if items, err := s.List(ctx, subject, ListFilter{}); err != nil || len(items) != 0 {
		t.Fatalf("List(after ACL removal) = %#v, error = %v, want empty", items, err)
	}
	_ = created
}

func TestListFiltersDeniedRowsBeforeApplyingLimit(t *testing.T) {
	f := newShareFixture(t)
	privateDir := filepath.Join(f.root, "private")
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(private) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile(secret) error = %v", err)
	}
	if _, err := f.db.Exec(`
INSERT INTO shares(id, public_id, secret_hash, creator_account_id, space_id, mount_id, relative_path, expires_at, created_at)
VALUES ('share-allowed-later', 'pub-allowed-later', ?, 'acct-manager', ?, ?, 'docs/readme.txt', ?, '2026-08-06T12:00:01Z'),
       ('share-denied-first', 'pub-denied-first', ?, 'acct-manager', ?, ?, 'private/secret.txt', ?, '2026-08-06T12:00:02Z')
`, share.HashSecret("allowed-secret"), f.spaceID, f.mountID, formatSQLiteTime(f.now.Add(time.Hour)), share.HashSecret("denied-secret"), f.spaceID, f.mountID, formatSQLiteTime(f.now.Add(time.Hour))); err != nil {
		t.Fatalf("insert ordered shares: %v", err)
	}
	subject := f.spaceGrantToken(t, domain.SpacePermissionEditor, []aitoken.FolderDeny{{
		MountID:             f.mountID,
		RelativePath:        "private",
		IdentityFingerprint: shareDirectoryFingerprint(t, privateDir),
	}})
	items, err := f.service().List(context.Background(), subject, ListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("List(Limit=1) error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "share-allowed-later" {
		t.Fatalf("List(Limit=1) = %#v, want allowed row after denied first row", items)
	}
}

func ptrInt64(value int64) *int64 { return &value }
