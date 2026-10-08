package memberfiles

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
	"omnora/internal/catalog"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/membershare"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

type readFixture struct {
	db      *sql.DB
	root    string
	spaceID string
	mountID string
}

func newReadFixture(t *testing.T) readFixture {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "memberfiles.db")})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	root, err := os.MkdirTemp(".", ".memberfiles-mount-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs(root) error = %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal(identity) error = %v", err)
	}
	if _, err := handle.SQL().Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-memberfiles', 'memberfiles@example.test', 'Member Files', 'member', 'active'),
       ('acct-other', 'other@example.test', 'Other', 'member', 'active');
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-memberfiles', 'shared', 'Member Files', 'acct-memberfiles', 'active'),
       ('space-hidden', 'shared', 'Hidden', 'acct-other', 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-memberfiles', 'acct-memberfiles', 'editor'),
       ('space-hidden', 'acct-other', 'viewer');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status, index_enabled, mount_identity_json)
VALUES ('mount-memberfiles', 'space-memberfiles', 'Files', ?, 'managed', 'read_write', 'active', 1, ?)
`, root, string(identityJSON)); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("Mkdir(docs) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs2"), 0o755); err != nil {
		t.Fatalf("Mkdir(docs2) error = %v", err)
	}
	writeReadFile(t, filepath.Join(root, "docs", "readme.txt"), "hello member")
	writeReadFile(t, filepath.Join(root, "docs2", "outside.txt"), "outside")
	writeReadFile(t, filepath.Join(root, "small.txt"), "small")
	writeReadFile(t, filepath.Join(root, "binary.bin"), string([]byte{0xff, 0xfe, 0xfd}))
	writeReadFile(t, filepath.Join(root, "nul.bin"), "before\x00after")
	writeReadFile(t, filepath.Join(root, "large.txt"), strings.Repeat("0123456789", 110000))
	writeReadFile(t, filepath.Join(root, "boundary-binary.bin"), strings.Repeat("a", int(DefaultReadTextBytes)-1)+string([]byte{0xff})+"tail")
	if err := os.Symlink(filepath.Join(root, "small.txt"), filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	return readFixture{db: handle.SQL(), root: root, spaceID: "space-memberfiles", mountID: "mount-memberfiles"}
}

func (f readFixture) service() *Service {
	return NewService(f.db, access.NewGuard(f.db), catalog.NewService(f.db))
}

func (f readFixture) locator(path string) access.Locator {
	return access.Locator{SpaceID: f.spaceID, MountID: f.mountID, Path: path}
}

func (f readFixture) session() access.Subject {
	return access.Subject{AccountID: "acct-memberfiles"}
}

func (f readFixture) token(t *testing.T, boundary string) access.Subject {
	return f.tokenWithScopes(t, boundary,
		aitoken.ScopeSpacesRead,
		aitoken.ScopeFilesList,
		aitoken.ScopeFilesMetadata,
		aitoken.ScopeFilesText,
		aitoken.ScopeSearchRead,
	)
}

func (f readFixture) tokenWithScopes(t *testing.T, boundary string, scopes ...aitoken.Scope) access.Subject {
	t.Helper()
	issued, err := aitoken.NewService(f.db).Create(context.Background(), aitoken.CreateRequest{
		AccountID:  "acct-memberfiles",
		Name:       "memberfiles read token",
		Scopes:     scopes,
		Boundaries: []aitoken.DirectoryBoundary{{SpaceID: f.spaceID, MountID: f.mountID, RelativePath: boundary}},
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create(token) error = %v", err)
	}
	principal := aitoken.Principal{
		AccountID:  issued.Token.AccountID,
		TokenID:    issued.Token.ID,
		PublicID:   issued.Token.PublicID,
		Scopes:     issued.Token.Scopes,
		Boundaries: issued.Token.Boundaries,
		ExpiresAt:  issued.Token.ExpiresAt,
	}
	return access.Subject{AccountID: principal.AccountID, Principal: &principal}
}

func (f readFixture) spaceGrantToken(t *testing.T, grants []aitoken.SpaceGrant, scopes ...aitoken.Scope) access.Subject {
	t.Helper()
	issued, err := aitoken.NewService(f.db).CreateSpaceGrant(context.Background(), aitoken.CreateSpaceGrantRequest{
		AccountID:   "acct-memberfiles",
		Name:        "memberfiles space grant token",
		Scopes:      scopes,
		SpaceGrants: grants,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
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

func memberfilesDirectoryFingerprint(t *testing.T, target string) string {
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

func insertMemberfilesMount(t *testing.T, f readFixture, mountID, spaceID string) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".memberfiles-future-mount-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs(root) error = %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("Capture(root) error = %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal(identity) error = %v", err)
	}
	if _, err := f.db.Exec(`
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status, index_enabled, mount_identity_json)
VALUES (?, ?, 'Future files', ?, 'managed', 'read_write', 'active', 1, ?)
`, mountID, spaceID, root, string(identityJSON)); err != nil {
		t.Fatalf("insert future mount: %v", err)
	}
	return root
}

func TestListSpacesMountsAndReadOperationsUseSessionACL(t *testing.T) {
	f := newReadFixture(t)
	s := f.service()
	ctx := context.Background()
	spaces, err := s.ListSpaces(ctx, f.session())
	if err != nil {
		t.Fatalf("ListSpaces() error = %v", err)
	}
	if len(spaces) != 1 || spaces[0].ID != f.spaceID {
		t.Fatalf("spaces = %#v, want only member space", spaces)
	}
	mounts, err := s.ListMounts(ctx, f.session(), f.spaceID)
	if err != nil {
		t.Fatalf("ListMounts() error = %v", err)
	}
	if len(mounts) != 1 || mounts[0].ID != f.mountID || mounts[0].ReadOnly || mounts[0].Name == "" {
		t.Fatalf("mounts = %#v", mounts)
	}
	payload, _ := json.Marshal(mounts)
	if strings.Contains(string(payload), f.root) {
		t.Fatalf("mount response leaked host root: %s", payload)
	}
	listing, err := s.List(ctx, f.session(), f.locator("docs"))
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "readme.txt" {
		t.Fatalf("List() = %#v, error = %v", listing, err)
	}
	metadata, err := s.Metadata(ctx, f.session(), f.locator("docs/readme.txt"))
	if err != nil || metadata.Kind != files.EntryKindFile || metadata.Size != int64(len("hello member")) {
		t.Fatalf("Metadata() = %#v, error = %v", metadata, err)
	}
	text, err := s.ReadText(ctx, f.session(), f.locator("small.txt"), 0)
	if err != nil || text.Text != "small" || text.Truncated {
		t.Fatalf("ReadText() = %#v, error = %v", text, err)
	}

	if _, err := f.db.Exec(`DELETE FROM space_members WHERE space_id = ? AND account_id = ?`, f.spaceID, "acct-memberfiles"); err != nil {
		t.Fatalf("remove ACL: %v", err)
	}
	if _, err := s.List(ctx, f.session(), f.locator("docs")); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("List() after ACL removal error = %v, want forbidden", err)
	}
}

func TestListSpacesUsesLegacyKindNameOrdering(t *testing.T) {
	f := newReadFixture(t)
	if _, err := f.db.Exec(`
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-order-personal', 'personal', 'Same Name', 'acct-memberfiles', 'active'),
       ('space-order-shared', 'shared', 'Same Name', 'acct-memberfiles', 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-order-personal', 'acct-memberfiles', 'viewer'),
       ('space-order-shared', 'acct-memberfiles', 'viewer')
`); err != nil {
		t.Fatalf("insert ordering spaces: %v", err)
	}
	spaces, err := f.service().ListSpaces(context.Background(), f.session())
	if err != nil {
		t.Fatalf("ListSpaces() error = %v", err)
	}
	indices := map[string]int{}
	for index, space := range spaces {
		indices[space.ID] = index
	}
	if indices["space-order-personal"] >= indices["space-order-shared"] {
		t.Fatalf("space order = %#v, want personal before shared for same name", spaces)
	}
}

func TestSpaceGrantCollectionsShowGrantedSpaceWithoutMountAndFutureMount(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-empty-granted', 'shared', 'Granted Empty', 'acct-memberfiles', 'active'),
       ('space-ungranted', 'shared', 'Ungranted', 'acct-memberfiles', 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-empty-granted', 'acct-memberfiles', 'viewer'),
       ('space-ungranted', 'acct-memberfiles', 'viewer');
`); err != nil {
		t.Fatalf("insert spaces: %v", err)
	}
	subject := f.spaceGrantToken(t, []aitoken.SpaceGrant{
		{SpaceID: f.spaceID, Permission: domain.SpacePermissionViewer},
		{SpaceID: "space-empty-granted", Permission: domain.SpacePermissionViewer},
	}, aitoken.ScopeSpacesRead)

	spaces, err := f.service().ListSpaces(ctx, subject)
	if err != nil {
		t.Fatalf("ListSpaces() error = %v", err)
	}
	spaceIDs := make(map[string]bool, len(spaces))
	for _, space := range spaces {
		spaceIDs[space.ID] = true
	}
	if len(spaces) != 2 || !spaceIDs[f.spaceID] || !spaceIDs["space-empty-granted"] || spaceIDs["space-ungranted"] {
		t.Fatalf("ListSpaces() = %#v, want only the two explicitly granted spaces", spaces)
	}
	if mounts, err := f.service().ListMounts(ctx, subject, "space-empty-granted"); err != nil || len(mounts) != 0 {
		t.Fatalf("ListMounts(empty granted space) = %#v, error = %v", mounts, err)
	}

	insertMemberfilesMount(t, f, "mount-future", "space-empty-granted")
	insertMemberfilesMount(t, f, "mount-ungranted", "space-ungranted")
	mounts, err := f.service().ListMounts(ctx, subject, "space-empty-granted")
	if err != nil || len(mounts) != 1 || mounts[0].ID != "mount-future" {
		t.Fatalf("ListMounts(after future mount) = %#v, error = %v", mounts, err)
	}
	if mounts, err := f.service().ListMounts(ctx, subject, "space-ungranted"); err != nil || len(mounts) != 0 {
		t.Fatalf("ListMounts(ungranted space) = %#v, error = %v, want empty", mounts, err)
	}
}

func TestSpaceGrantDirectoryAndSearchFilterDeniedSubtreeWithoutConsumingPage(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	privateDir := filepath.Join(f.root, "docs", "private")
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(private) error = %v", err)
	}
	writeReadFile(t, filepath.Join(privateDir, "a-secret.txt"), "secret")
	writeReadFile(t, filepath.Join(f.root, "docs", "b-public.txt"), "public b")
	writeReadFile(t, filepath.Join(f.root, "docs", "c-public.txt"), "public c")
	modifiedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`
INSERT INTO catalog_entries(id, space_id, mount_id, relative_path, name, entry_kind, preview_kind, size_bytes, modified_at, identity_fingerprint)
VALUES ('entry-denied', ?, ?, 'docs/private/a-secret.txt', 'a-secret.txt', 'file', 'text', 6, ?, 'denied'),
       ('entry-public-b', ?, ?, 'docs/b-public.txt', 'b-public.txt', 'file', 'text', 8, ?, 'public-b'),
       ('entry-public-c', ?, ?, 'docs/c-public.txt', 'c-public.txt', 'file', 'text', 8, ?, 'public-c')
`, f.spaceID, f.mountID, modifiedAt, f.spaceID, f.mountID, modifiedAt, f.spaceID, f.mountID, modifiedAt); err != nil {
		t.Fatalf("insert catalog rows: %v", err)
	}
	subject := f.spaceGrantToken(t, []aitoken.SpaceGrant{{
		SpaceID:    f.spaceID,
		Permission: domain.SpacePermissionViewer,
		FolderDenylist: []aitoken.FolderDeny{{
			MountID:             f.mountID,
			RelativePath:        "docs/private",
			IdentityFingerprint: memberfilesDirectoryFingerprint(t, privateDir),
		}},
	}}, aitoken.ScopeFilesList, aitoken.ScopeSearchRead)

	listing, err := f.service().List(ctx, subject, f.locator("docs"))
	if err != nil {
		t.Fatalf("List(docs) error = %v", err)
	}
	for _, entry := range listing.Entries {
		if entry.Name == "private" || strings.Contains(entry.RelativePath, "private") {
			t.Fatalf("List(docs) leaked denied child: %#v", listing.Entries)
		}
	}

	first, err := f.service().Search(ctx, subject, SearchRequest{SpaceID: f.spaceID, Query: ".txt", Limit: 1})
	if err != nil {
		t.Fatalf("Search(first page) error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].RelativePath != "docs/b-public.txt" || first.NextCursor == "" {
		t.Fatalf("Search(first page) = %#v, want first authorized item and cursor", first)
	}
	second, err := f.service().Search(ctx, subject, SearchRequest{SpaceID: f.spaceID, Query: ".txt", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("Search(second page) error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].RelativePath != "docs/c-public.txt" {
		t.Fatalf("Search(second page) = %#v, want second authorized item", second)
	}
	for _, result := range append(first.Items, second.Items...) {
		if strings.Contains(result.Name, "secret") || strings.Contains(result.RelativePath, "private") {
			t.Fatalf("Search leaked denied path: %#v", result)
		}
	}
}

func TestSharesCreatePreviewMatchesSpaceGrantCreateAndWritableMount(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE space_members SET permission = 'manager' WHERE space_id = ? AND account_id = ?`, f.spaceID, "acct-memberfiles"); err != nil {
		t.Fatalf("promote account to manager: %v", err)
	}
	locator := f.locator("docs/readme.txt")
	filesService := f.service()
	sharesService := membershare.NewService(f.db, access.NewGuard(f.db))
	if _, err := filesService.Preview(ctx, f.session(), locator, aitoken.ScopeSharesCreate); err != nil {
		t.Fatalf("browser Preview(shares:create) error = %v", err)
	}
	if _, err := sharesService.Create(ctx, f.session(), membershare.CreateRequest{Locator: locator}); err != nil {
		t.Fatalf("browser Create(shares:create) error = %v", err)
	}

	tokenSubject := f.spaceGrantToken(t, []aitoken.SpaceGrant{{
		SpaceID: f.spaceID, Permission: domain.SpacePermissionEditor,
	}}, aitoken.ScopeSharesCreate)
	if _, err := filesService.Preview(ctx, tokenSubject, locator, aitoken.ScopeSharesCreate); err != nil {
		t.Fatalf("space-grant Preview(shares:create) error = %v", err)
	}
	if _, err := sharesService.Create(ctx, tokenSubject, membershare.CreateRequest{Locator: locator}); err != nil {
		t.Fatalf("space-grant Create(shares:create) error = %v", err)
	}

	if _, err := f.db.Exec(`UPDATE mounts SET mode = 'read_only' WHERE id = ?`, f.mountID); err != nil {
		t.Fatalf("set mount read-only: %v", err)
	}
	if _, err := filesService.Preview(ctx, tokenSubject, locator, aitoken.ScopeSharesCreate); !errors.Is(err, access.ErrReadonlyMount) {
		t.Fatalf("space-grant Preview(read-only mount) error = %v, want readonly", err)
	}
	if _, err := sharesService.Create(ctx, tokenSubject, membershare.CreateRequest{Locator: locator}); !errors.Is(err, access.ErrReadonlyMount) {
		t.Fatalf("space-grant Create(read-only mount) error = %v, want readonly", err)
	}
}

func TestTokenBoundaryFiltersAdjacentPrefixesAndCatalogResults(t *testing.T) {
	f := newReadFixture(t)
	ctx := context.Background()
	if _, err := catalog.NewService(f.db).ScanMount(ctx, catalog.Mount{
		ID: f.mountID, SpaceID: f.spaceID, Root: f.root,
		Status: catalog.MountStatusActive, IndexEnabled: true, IdentityVerified: true,
	}, catalog.ScanOptions{}); err != nil {
		t.Fatalf("ScanMount() error = %v", err)
	}
	if _, err := f.db.Exec(`
INSERT INTO catalog_entries(
  id, space_id, mount_id, relative_path, name, entry_kind, preview_kind,
  size_bytes, modified_at, identity_fingerprint, indexed_at, deleted_at
) VALUES
  ('evil-dotdot', ?, ?, 'docs/../outside.txt', 'outside.txt', 'file', 'text', 7, '2026-08-06T15:06:37Z', 'evil-dotdot', CURRENT_TIMESTAMP, NULL),
  ('evil-absolute', ?, ?, '/host.txt', 'host.txt', 'file', 'text', 7, '2026-08-06T15:06:37Z', 'evil-absolute', CURRENT_TIMESTAMP, NULL),
  ('evil-reserved', ?, ?, '.omnora/secret.txt', 'secret.txt', 'file', 'text', 7, '2026-08-06T15:06:37Z', 'evil-reserved', CURRENT_TIMESTAMP, NULL)
`, f.spaceID, f.mountID, f.spaceID, f.mountID, f.spaceID, f.mountID); err != nil {
		t.Fatalf("insert malformed catalog rows: %v", err)
	}
	s := f.service()
	subject := f.token(t, "docs")
	if mounts, err := s.ListMounts(ctx, subject, f.spaceID); err != nil || len(mounts) != 1 {
		t.Fatalf("ListMounts(token) = %#v, error = %v", mounts, err)
	}
	listing, err := s.List(ctx, subject, f.locator("docs"))
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "readme.txt" {
		t.Fatalf("List(token) = %#v, error = %v", listing, err)
	}
	if _, err := s.List(ctx, subject, f.locator("docs2")); !errors.Is(err, access.ErrBoundaryViolation) {
		t.Fatalf("List(adjacent boundary) error = %v, want boundary violation", err)
	}
	result, err := s.Search(ctx, subject, SearchRequest{SpaceID: f.spaceID, Query: ".txt", Limit: 50})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].RelativePath != "docs/readme.txt" {
		t.Fatalf("Search() items = %#v, want only docs/readme.txt", result.Items)
	}
	spaces, err := s.ListSpaces(ctx, subject)
	if err != nil || len(spaces) != 1 || spaces[0].ID != f.spaceID {
		t.Fatalf("ListSpaces(token) = %#v, error = %v", spaces, err)
	}
	withoutSpacesScope := f.tokenWithScopes(t, "docs", aitoken.ScopeSearchRead)
	if _, err := s.ListMounts(ctx, withoutSpacesScope, f.spaceID); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("ListMounts(missing scope) error = %v, want forbidden", err)
	}
	withoutSearchScope := f.tokenWithScopes(t, "docs", aitoken.ScopeSpacesRead)
	if _, err := s.Search(ctx, withoutSearchScope, SearchRequest{SpaceID: f.spaceID, Query: ".txt"}); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("Search(missing scope) error = %v, want forbidden", err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET status = 'disabled' WHERE id = ?`, f.mountID); err != nil {
		t.Fatalf("disable mount: %v", err)
	}
	if spaces, err := s.ListSpaces(ctx, subject); err != nil || len(spaces) != 0 {
		t.Fatalf("ListSpaces(disabled mount) = %#v, error = %v, want empty", spaces, err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET status = 'active', mount_identity_json = '{}' WHERE id = ?`, f.mountID); err != nil {
		t.Fatalf("set drifted identity: %v", err)
	}
	if spaces, err := s.ListSpaces(ctx, subject); err != nil || len(spaces) != 0 {
		t.Fatalf("ListSpaces(drifted identity) = %#v, error = %v, want empty", spaces, err)
	}
}

func TestReadTextLimitsUTF8BinaryDirectoryAndSymlink(t *testing.T) {
	f := newReadFixture(t)
	s := f.service()
	ctx := context.Background()
	session := f.session()
	defaultResult, err := s.ReadText(ctx, session, f.locator("large.txt"), 0)
	if err != nil {
		t.Fatalf("ReadText(default) error = %v", err)
	}
	if defaultResult.BytesRead != DefaultReadTextBytes || !defaultResult.Truncated {
		t.Fatalf("default result = %#v, want 64KiB truncated", defaultResult)
	}
	hardResult, err := s.ReadText(ctx, session, f.locator("large.txt"), MaxReadTextBytes*2)
	if err != nil {
		t.Fatalf("ReadText(hard limit) error = %v", err)
	}
	if hardResult.BytesRead > MaxReadTextBytes || !hardResult.Truncated {
		t.Fatalf("hard result bytes=%d truncated=%v, want <=1MiB truncated", hardResult.BytesRead, hardResult.Truncated)
	}
	if _, err := s.ReadText(ctx, session, f.locator("binary.bin"), 0); !errors.Is(err, ErrNotTextFile) {
		t.Fatalf("ReadText(binary) error = %v, want ErrNotTextFile", err)
	}
	if _, err := s.ReadText(ctx, session, f.locator("boundary-binary.bin"), 0); !errors.Is(err, ErrNotTextFile) {
		t.Fatalf("ReadText(boundary binary) error = %v, want ErrNotTextFile", err)
	}
	if _, err := s.ReadText(ctx, session, f.locator("nul.bin"), 0); !errors.Is(err, ErrNotTextFile) {
		t.Fatalf("ReadText(NUL) error = %v, want ErrNotTextFile", err)
	}
	if _, err := s.ReadText(ctx, session, f.locator("docs"), 0); !errors.Is(err, files.ErrNotFile) {
		t.Fatalf("ReadText(directory) error = %v, want ErrNotFile", err)
	}
	if _, err := s.ReadText(ctx, session, f.locator("linked.txt"), 0); !errors.Is(err, access.ErrBoundaryViolation) {
		t.Fatalf("ReadText(symlink) error = %v, want boundary violation", err)
	}
	if _, err := s.ReadText(ctx, session, f.locator("../small.txt"), 0); err == nil {
		t.Fatal("ReadText(path traversal) unexpectedly succeeded")
	}
}

func writeReadFile(t *testing.T, name, value string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(value), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}
