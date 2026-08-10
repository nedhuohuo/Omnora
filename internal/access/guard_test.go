package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

type guardFixture struct {
	db             *sql.DB
	personalRoot   string
	commonRoot     string
	commonIdentity string
}

func newGuardFixture(t *testing.T) guardFixture {
	t.Helper()
	dbHandle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "guard.db")})
	if err != nil {
		t.Fatalf("open target-shape database: %v", err)
	}
	t.Cleanup(func() { _ = dbHandle.Close() })
	db := dbHandle.SQL()

	personalRoot := mkdirAndCaptureRoot(t, "personal")
	commonRoot := mkdirAndCaptureRoot(t, "common")
	readonlyRoot := mkdirAndCaptureRoot(t, "readonly")
	restrictedRoot := mkdirAndCaptureRoot(t, "restricted")
	personalIdentity := identityJSON(t, personalRoot)
	commonIdentity := identityJSON(t, commonRoot)

	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-owner', 'owner@example.test', 'Owner', 'member', 'active'),
       ('acct-other', 'other@example.test', 'Other', 'member', 'active'),
       ('acct-admin', 'admin@example.test', 'Admin', 'admin', 'active'),
       ('acct-disabled', 'disabled@example.test', 'Disabled', 'member', 'disabled')
`); err != nil {
		t.Fatalf("insert accounts: %v", err)
	}
	for _, accountID := range []string{"acct-owner", "acct-other", "acct-admin", "acct-disabled"} {
		if err := os.Mkdir(filepath.Join(personalRoot, accountID), 0o700); err != nil {
			t.Fatalf("create personal directory for %s: %v", accountID, err)
		}
	}
	if _, err := db.Exec(`
INSERT INTO personal_directories(account_id, relative_path, state)
VALUES ('acct-owner', 'acct-owner', 'ready'),
       ('acct-other', 'acct-other', 'ready'),
       ('acct-admin', 'acct-admin', 'ready'),
       ('acct-disabled', 'acct-disabled', 'ready')
`); err != nil {
		t.Fatalf("insert personal directories: %v", err)
	}
	if _, err := db.Exec(`
UPDATE mounts
SET root_path = 'personal', mount_identity_json = ?, status = 'active'
WHERE id = 'personal-default'
`, personalIdentity); err != nil {
		t.Fatalf("bind personal default identity: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json)
VALUES ('common-rw', 'Common RW', ?, 'common', 'external', 'normal', 'read_write', 'active', ?),
       ('common-ro', 'Common RO', ?, 'common', 'external', 'normal', 'read_only', 'active', ?),
       ('common-restricted', 'Restricted', ?, 'common', 'external', 'restricted', 'read_write', 'active', ?)
`, commonRoot, commonIdentity, readonlyRoot, identityJSON(t, readonlyRoot), restrictedRoot, identityJSON(t, restrictedRoot)); err != nil {
		t.Fatalf("insert common mounts: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO mount_grants(mount_id, account_id, permission)
VALUES ('common-rw', 'acct-owner', 'editor'),
       ('common-rw', 'acct-other', 'viewer'),
       ('common-ro', 'acct-owner', 'editor')
`); err != nil {
		t.Fatalf("insert grants: %v", err)
	}
	return guardFixture{db: db, personalRoot: personalRoot, commonRoot: commonRoot, commonIdentity: commonIdentity}
}

func mkdirAndCaptureRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create %s root: %v", name, err)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("absolute %s root: %v", name, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve %s root: %v", name, err)
	}
	return root
}

func identityJSON(t *testing.T, root string) string {
	t.Helper()
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture identity for %s: %v", root, err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity for %s: %v", root, err)
	}
	return string(encoded)
}

func check(accountID string, locator Locator, permission domain.ContentPermission, write bool) CheckRequest {
	return CheckRequest{
		Subject:            Subject{AccountID: accountID},
		Scope:              aitoken.ScopeFilesList,
		Locator:            locator,
		RequiredPermission: permission,
		Write:              write,
	}
}

func issueToken(t *testing.T, f guardFixture, accountID string, scopes []aitoken.Scope, boundaries []aitoken.DirectoryBoundary) aitoken.Principal {
	t.Helper()
	issued, err := aitoken.NewService(f.db).Create(context.Background(), aitoken.CreateRequest{
		AccountID:  accountID,
		Name:       "guard-test",
		Scopes:     scopes,
		Boundaries: boundaries,
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return aitoken.Principal{AccountID: accountID, TokenID: issued.Token.ID}
}

func TestAuthorizePersonalCropsEveryAccountToItsOwnDirectory(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)

	owner, err := guard.Authorize(context.Background(), check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "docs/report.txt"}, domain.ContentPermissionViewer, false))
	if err != nil {
		t.Fatalf("authorize owner personal file: %v", err)
	}
	if owner.Source != contentref.SourcePersonal || owner.ID != "personal-default" || owner.Root != filepath.Join(f.personalRoot, "acct-owner") || owner.MountRoot != f.personalRoot || owner.StorageKind != domain.StorageKindManaged || owner.Mode != domain.MountModeReadWrite || owner.RelativePath != "docs/report.txt" || owner.StorageRelativePath != "acct-owner/docs/report.txt" {
		t.Fatalf("owner authorized mount = %#v", owner)
	}
	other, err := guard.Authorize(context.Background(), check("acct-other", Locator{Source: contentref.SourcePersonal, Path: "."}, domain.ContentPermissionEditor, true))
	if err != nil {
		t.Fatalf("authorize other personal root: %v", err)
	}
	if other.Root != filepath.Join(f.personalRoot, "acct-other") || other.StorageRelativePath != "acct-other" {
		t.Fatalf("other authorized mount = %#v", other)
	}
	if _, err := guard.Authorize(context.Background(), check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "../acct-other/secret"}, domain.ContentPermissionViewer, false)); !errors.Is(err, ErrBoundaryViolation) {
		t.Fatalf("personal sibling traversal error = %v, want boundary violation", err)
	}
	if _, err := guard.Authorize(context.Background(), check("acct-disabled", Locator{Source: contentref.SourcePersonal, Path: "."}, domain.ContentPermissionViewer, false)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled account error = %v, want unauthorized", err)
	}
}

func TestAuthorizeCommonMountRequiresLiveGrantRegardlessOfAdminRole(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	locator := Locator{Source: contentref.SourceCommonMount, MountID: "common-rw", Path: "projects"}

	if _, err := guard.Authorize(context.Background(), check("acct-admin", locator, domain.ContentPermissionViewer, false)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ungranted admin error = %v, want forbidden", err)
	}
	viewer, err := guard.Authorize(context.Background(), check("acct-other", locator, domain.ContentPermissionViewer, false))
	if err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if viewer.Source != contentref.SourceCommonMount || viewer.Root != f.commonRoot || viewer.MountRoot != f.commonRoot || viewer.StorageRelativePath != "projects" {
		t.Fatalf("viewer authorized mount = %#v", viewer)
	}
	if _, err := guard.Authorize(context.Background(), check("acct-other", locator, domain.ContentPermissionEditor, true)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer write error = %v, want forbidden", err)
	}
	if _, err := f.db.Exec(`DELETE FROM mount_grants WHERE mount_id = 'common-rw' AND account_id = 'acct-other'`); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if _, err := guard.Authorize(context.Background(), check("acct-other", locator, domain.ContentPermissionViewer, false)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked grant error = %v, want forbidden", err)
	}
	unknown := Locator{Source: contentref.SourceCommonMount, MountID: "common-restricted", Path: "."}
	if _, err := guard.Authorize(context.Background(), check("acct-admin", unknown, domain.ContentPermissionViewer, false)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("restricted mount probe error = %v, want same forbidden result", err)
	}
	readonly := Locator{Source: contentref.SourceCommonMount, MountID: "common-ro", Path: "."}
	if _, err := guard.Authorize(context.Background(), check("acct-owner", readonly, domain.ContentPermissionEditor, true)); !errors.Is(err, ErrReadonlyMount) {
		t.Fatalf("read-only editor write error = %v, want readonly", err)
	}
}

func TestAuthorizeTokenIntersectsFreshScopeBoundaryAndLiveGrant(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	ctx := context.Background()

	personal := issueToken(t, f, "acct-owner", []aitoken.Scope{aitoken.ScopeFilesList}, []aitoken.DirectoryBoundary{{Source: contentref.SourcePersonal, RelativePath: "docs"}})
	req := check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "docs/report.txt"}, domain.ContentPermissionViewer, false)
	req.Subject.Principal = &personal
	if _, err := guard.Authorize(ctx, req); err != nil {
		t.Fatalf("personal-bound token: %v", err)
	}
	req.Locator.Path = "docs-private/report.txt"
	if _, err := guard.Authorize(ctx, req); !errors.Is(err, ErrBoundaryViolation) {
		t.Fatalf("adjacent personal boundary error = %v", err)
	}
	req.Scope = aitoken.ScopeFilesText
	req.Locator.Path = "docs/report.txt"
	if _, err := guard.Authorize(ctx, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing token scope error = %v", err)
	}
	common := issueToken(t, f, "acct-owner", []aitoken.Scope{aitoken.ScopeFilesList}, []aitoken.DirectoryBoundary{{Source: contentref.SourceCommonMount, MountID: "common-rw", RelativePath: "projects"}})
	commonReq := check("acct-owner", Locator{Source: contentref.SourceCommonMount, MountID: "common-rw", Path: "projects/spec.md"}, domain.ContentPermissionViewer, false)
	commonReq.Subject.Principal = &common
	if _, err := guard.Authorize(ctx, commonReq); err != nil {
		t.Fatalf("common-bound token: %v", err)
	}
	commonReq.Locator.Path = "projects-private/spec.md"
	if _, err := guard.Authorize(ctx, commonReq); !errors.Is(err, ErrBoundaryViolation) {
		t.Fatalf("adjacent common boundary error = %v", err)
	}
	commonReq.Locator = Locator{Source: contentref.SourceCommonMount, MountID: "common-ro", Path: "projects/spec.md"}
	if _, err := guard.Authorize(ctx, commonReq); !errors.Is(err, ErrBoundaryViolation) {
		t.Fatalf("different common mount boundary error = %v", err)
	}

	all := issueToken(t, f, "acct-owner", []aitoken.Scope{aitoken.ScopeFilesList}, []aitoken.DirectoryBoundary{{Source: aitoken.SourceAllAccountContent}})
	allCommon := check("acct-owner", Locator{Source: contentref.SourceCommonMount, MountID: "common-rw", Path: "team"}, domain.ContentPermissionViewer, false)
	allCommon.Subject.Principal = &all
	if _, err := guard.Authorize(ctx, allCommon); err != nil {
		t.Fatalf("all-account token common mount: %v", err)
	}
	if _, err := f.db.Exec(`DELETE FROM mount_grants WHERE mount_id = 'common-rw' AND account_id = 'acct-owner'`); err != nil {
		t.Fatalf("revoke live common grant: %v", err)
	}
	if _, err := guard.Authorize(ctx, allCommon); !errors.Is(err, ErrForbidden) {
		t.Fatalf("all-account token after revoke error = %v, want forbidden", err)
	}
	allPersonal := check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "notes"}, domain.ContentPermissionViewer, false)
	allPersonal.Subject.Principal = &all
	if _, err := guard.Authorize(ctx, allPersonal); err != nil {
		t.Fatalf("all-account token personal after common revoke: %v", err)
	}
}

func TestAuthorizeRejectsCollaborationAndDamagedTokenPrincipal(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	ctx := context.Background()

	collaboration := Locator{Source: contentref.SourceCollaboration, CollaborationID: "collab-1", Path: "."}
	automation := check("acct-owner", collaboration, domain.ContentPermissionViewer, false)
	automation.Subject.Principal = &aitoken.Principal{AccountID: "acct-owner", TokenID: "token-1"}
	if _, err := guard.Authorize(ctx, automation); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("automation collaboration error = %v, want invalid request", err)
	}
	allContent := Locator{Source: aitoken.SourceAllAccountContent, Path: "."}
	if _, err := guard.Authorize(ctx, check("acct-owner", allContent, domain.ContentPermissionViewer, false)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("all-account locator error = %v, want invalid request", err)
	}
	empty := aitoken.Principal{}
	req := check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "."}, domain.ContentPermissionViewer, false)
	req.Subject.Principal = &empty
	if _, err := guard.Authorize(ctx, req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty principal error = %v, want unauthorized", err)
	}
	principal := issueToken(t, f, "acct-owner", []aitoken.Scope{aitoken.ScopeFilesList}, []aitoken.DirectoryBoundary{{Source: contentref.SourcePersonal}})
	if _, err := f.db.Exec(`DELETE FROM ai_token_boundaries WHERE token_id = ?`, principal.TokenID); err != nil {
		t.Fatalf("damage token boundaries: %v", err)
	}
	req.Subject.Principal = &principal
	if _, err := guard.Authorize(ctx, req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("damaged stored principal error = %v, want unauthorized", err)
	}
}

func TestAuthorizeRejectsSymlinksAndIdentityDriftForPersonalAndCommon(t *testing.T) {
	t.Run("personal symlink", func(t *testing.T) {
		f := newGuardFixture(t)
		if err := os.Symlink(filepath.Join(f.personalRoot, "acct-other"), filepath.Join(f.personalRoot, "acct-owner", "alias")); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		_, err := NewGuard(f.db).Authorize(context.Background(), check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "alias/secret"}, domain.ContentPermissionViewer, false))
		if !errors.Is(err, ErrBoundaryViolation) {
			t.Fatalf("personal symlink error = %v, want boundary violation", err)
		}
	})
	t.Run("personal identity drift", func(t *testing.T) {
		f := newGuardFixture(t)
		oldRoot := f.personalRoot + "-old"
		if err := os.Rename(f.personalRoot, oldRoot); err != nil {
			t.Fatalf("move original personal root: %v", err)
		}
		if err := os.Mkdir(f.personalRoot, 0o700); err != nil {
			t.Fatalf("replace personal root: %v", err)
		}
		_, err := NewGuard(f.db).Authorize(context.Background(), check("acct-owner", Locator{Source: contentref.SourcePersonal, Path: "."}, domain.ContentPermissionViewer, false))
		if !errors.Is(err, ErrMountIdentityUnverifiable) {
			t.Fatalf("personal identity drift error = %v", err)
		}
	})
	t.Run("common identity drift", func(t *testing.T) {
		f := newGuardFixture(t)
		oldRoot := f.commonRoot + "-old"
		if err := os.Rename(f.commonRoot, oldRoot); err != nil {
			t.Fatalf("move original common root: %v", err)
		}
		if err := os.Mkdir(f.commonRoot, 0o700); err != nil {
			t.Fatalf("replace common root: %v", err)
		}
		_, err := NewGuard(f.db).Authorize(context.Background(), check("acct-owner", Locator{Source: contentref.SourceCommonMount, MountID: "common-rw", Path: "."}, domain.ContentPermissionViewer, false))
		if !errors.Is(err, ErrMountIdentityUnverifiable) {
			t.Fatalf("common identity drift error = %v", err)
		}
	})
}

func TestLoadMountIdentityUsesMountIDWithoutLegacyCoordinates(t *testing.T) {
	f := newGuardFixture(t)
	mount, err := NewGuard(f.db).LoadMountIdentity(context.Background(), "common-rw")
	if err != nil {
		t.Fatalf("load mount identity: %v", err)
	}
	if mount.ID != "common-rw" || mount.MountRoot != f.commonRoot || mount.IdentityJSON != f.commonIdentity {
		t.Fatalf("loaded mount = %#v", mount)
	}
}
