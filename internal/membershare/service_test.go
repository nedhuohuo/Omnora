package membershare

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
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

func ptrInt64(value int64) *int64 { return &value }
