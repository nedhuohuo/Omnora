package memberfiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/catalog"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

type memberFixture struct {
	db           *sql.DB
	personalRoot string
	commonRoot   string
	service      *Service
}

func newMemberFixture(t *testing.T) memberFixture {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "member.db")})
	if err != nil {
		t.Fatalf("open target database: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	db := handle.SQL()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	personalRoot := filepath.Join(base, "personal")
	commonRoot := filepath.Join(base, "common")
	for _, root := range []string{personalRoot, commonRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("create root: %v", err)
		}
	}
	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-1', 'one@example.test', 'One', 'member', 'active'),
       ('acct-2', 'two@example.test', 'Two', 'member', 'active'),
       ('acct-admin', 'admin@example.test', 'Admin', 'admin', 'active');
INSERT INTO personal_directories(account_id, relative_path, state)
VALUES ('acct-1', 'acct-1', 'ready'), ('acct-2', 'acct-2', 'ready'), ('acct-admin', 'acct-admin', 'ready')
`); err != nil {
		t.Fatalf("insert accounts: %v", err)
	}
	for _, accountID := range []string{"acct-1", "acct-2", "acct-admin"} {
		if err := os.Mkdir(filepath.Join(personalRoot, accountID), 0o700); err != nil {
			t.Fatalf("create account root: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE mounts SET mount_identity_json = ?, index_enabled = 1 WHERE id = 'personal-default'`, memberIdentity(t, personalRoot)); err != nil {
		t.Fatalf("bind personal identity: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status, mount_identity_json)
VALUES ('common-1', 'Team files', ?, 'common', 'external', 'normal', 'read_write', 1, 'active', ?);
INSERT INTO mount_grants(mount_id, account_id, permission)
VALUES ('common-1', 'acct-1', 'editor'), ('common-1', 'acct-2', 'viewer')
`, commonRoot, memberIdentity(t, commonRoot)); err != nil {
		t.Fatalf("insert common mount: %v", err)
	}
	if err := os.Mkdir(filepath.Join(personalRoot, "acct-1", "docs"), 0o700); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(personalRoot, "acct-1", "docs", "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write personal file: %v", err)
	}
	return memberFixture{db: db, personalRoot: personalRoot, commonRoot: commonRoot, service: NewService(db, access.NewGuard(db), catalog.NewService(db))}
}

func memberIdentity(t *testing.T, root string) string {
	t.Helper()
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture identity: %v", err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	return string(encoded)
}

func memberSubject(accountID string) access.Subject { return access.Subject{AccountID: accountID} }

func personalLocator(value string) access.Locator {
	return access.Locator{Source: contentref.SourcePersonal, Path: value}
}

func TestListMountsAndPersonalReadHaveNoSpaceOrDefaultMountLeak(t *testing.T) {
	f := newMemberFixture(t)
	mounts, err := f.service.ListMounts(context.Background(), memberSubject("acct-1"))
	if err != nil {
		t.Fatalf("ListMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].ID != "common-1" || mounts[0].Permission != domain.ContentPermissionEditor {
		t.Fatalf("mounts = %#v", mounts)
	}
	payload, _ := json.Marshal(mounts)
	if strings.Contains(string(payload), "personal-default") || strings.Contains(string(payload), "space") || strings.Contains(string(payload), f.commonRoot) {
		t.Fatalf("mount payload leaked protected coordinates: %s", payload)
	}
	listing, err := f.service.List(context.Background(), memberSubject("acct-1"), personalLocator("docs"))
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "readme.txt" {
		t.Fatalf("List = %#v, err=%v", listing, err)
	}
	if _, err := f.service.List(context.Background(), memberSubject("acct-2"), personalLocator("docs")); err == nil {
		t.Fatal("second account read first account directory")
	}
}

func TestPrepareUploadStoresMountGlobalPathWithoutSpace(t *testing.T) {
	f := newMemberFixture(t)
	result, err := f.service.PrepareUpload(context.Background(), memberSubject("acct-1"), UploadRequest{
		Locator: personalLocator("upload.txt"), ExpectedSize: 3,
	})
	if err != nil {
		t.Fatalf("PrepareUpload: %v", err)
	}
	var mountID, target string
	if err := f.db.QueryRow(`SELECT mount_id, target_relative_path FROM upload_sessions WHERE id = ?`, result.ID).Scan(&mountID, &target); err != nil {
		t.Fatalf("read upload row: %v", err)
	}
	if mountID != "personal-default" || target != "acct-1/upload.txt" {
		t.Fatalf("upload coordinate = %q %q", mountID, target)
	}
}

func TestSearchAllAccountContentStripsOnlyCurrentPersonalPrefix(t *testing.T) {
	f := newMemberFixture(t)
	if _, err := f.db.Exec(`
INSERT INTO catalog_entries(id, mount_id, relative_path, name, entry_kind, preview_kind, size_bytes, modified_at, identity_fingerprint)
VALUES ('mine', 'personal-default', 'acct-1/docs/readme.txt', 'readme.txt', 'file', 'text', 5, '2026-08-08T00:00:00Z', 'mine'),
       ('other', 'personal-default', 'acct-2/secret.txt', 'secret.txt', 'file', 'text', 5, '2026-08-08T00:00:00Z', 'other'),
       ('common', 'common-1', 'team.txt', 'team.txt', 'file', 'text', 5, '2026-08-08T00:00:00Z', 'common')
`); err != nil {
		t.Fatalf("insert catalog: %v", err)
	}
	result, err := f.service.Search(context.Background(), memberSubject("acct-1"), SearchRequest{Source: aitoken.SourceAllAccountContent})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %#v", result.Items)
	}
	paths := map[string]string{}
	for _, item := range result.Items {
		paths[string(item.Source)] = item.RelativePath
		if item.Source == contentref.SourcePersonal && item.MountID != "" {
			t.Fatalf("personal search leaked default mount ID: %#v", item)
		}
	}
	if paths["personal"] != "docs/readme.txt" || paths["common_mount"] != "team.txt" {
		t.Fatalf("paths = %#v", paths)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(payload), "personal-default") {
		t.Fatalf("personal search payload leaked default mount ID: %s", payload)
	}
}

func TestPersonalTrashIsAccountIsolated(t *testing.T) {
	f := newMemberFixture(t)
	trashed, err := f.service.Trash(context.Background(), memberSubject("acct-1"), personalLocator("docs/readme.txt"))
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}
	listed, err := f.service.ListTrash(context.Background(), memberSubject("acct-1"), personalLocator("."))
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != trashed.TrashID {
		t.Fatalf("owner trash = %#v, err=%v", listed, err)
	}
	other, err := f.service.ListTrash(context.Background(), memberSubject("acct-2"), personalLocator("."))
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("other trash = %#v, err=%v", other, err)
	}
}

func TestCommonMountDeleteUsesDeletingAccountsPersonalTrash(t *testing.T) {
	f := newMemberFixture(t)
	if err := os.WriteFile(filepath.Join(f.commonRoot, "team.txt"), []byte("team"), 0o600); err != nil {
		t.Fatalf("write common file: %v", err)
	}
	common := access.Locator{Source: contentref.SourceCommonMount, MountID: "common-1", Path: "team.txt"}
	trashed, err := f.service.Trash(context.Background(), memberSubject("acct-1"), common)
	if err != nil {
		t.Fatalf("Trash common file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.commonRoot, "team.txt")); !os.IsNotExist(err) {
		t.Fatalf("common source remains after verified trash copy: %v", err)
	}
	listed, err := f.service.ListTrash(context.Background(), memberSubject("acct-1"), personalLocator("."))
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != trashed.TrashID {
		t.Fatalf("personal trash = %#v, err=%v", listed, err)
	}
	restored, err := f.service.RestoreTrash(context.Background(), memberSubject("acct-1"), personalLocator("."), trashed.TrashID)
	if err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}
	if !strings.HasPrefix(restored.RelativePath, "Recovered Files/") {
		t.Fatalf("restored path = %q, want personal recovery directory", restored.RelativePath)
	}
}
