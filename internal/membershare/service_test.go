package membershare

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/access"
	"omnora/internal/contentref"
	"omnora/internal/mountid"
	"omnora/internal/share"
	"omnora/internal/store"
)

type shareFixture struct {
	db           *sql.DB
	commonRoot   string
	personalRoot string
	now          time.Time
}

func newShareFixture(t *testing.T) shareFixture {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "membershare.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	db := handle.SQL()
	commonRoot := realDir(t, "common")
	managedRoot := realDir(t, "managed")
	personalRoot := filepath.Join(managedRoot, "personal")
	if err := os.Mkdir(personalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"owner", "viewer"} {
		if err := os.MkdirAll(filepath.Join(personalRoot, account, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(personalRoot, account, "docs", "private.txt"), []byte(account), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(commonRoot, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonRoot, "docs", "shared.txt"), []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	commonIdentity := identityJSON(t, commonRoot)
	personalIdentity := identityJSON(t, personalRoot)
	_, err = db.Exec(`
INSERT INTO accounts(id,email,display_name,role,status) VALUES
 ('owner','owner@example.test','Owner','member','active'),
 ('viewer','viewer@example.test','Viewer','member','active');
INSERT INTO personal_directories(account_id,relative_path,state) VALUES
	 ('owner','owner','ready'),('viewer','viewer','ready');
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE mounts SET mount_identity_json=? WHERE id='personal-default'`, personalIdentity); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO mounts(id,display_name,root_path,purpose,storage_kind,governance,mode,index_enabled,share_enabled,status,mount_identity_json)
VALUES ('common','Common',?,'common','external','normal','read_write',1,1,'active',?);
INSERT INTO mount_grants(mount_id,account_id,permission) VALUES
 ('common','owner','editor'),('common','viewer','viewer');
`, commonRoot, commonIdentity)
	if err != nil {
		t.Fatal(err)
	}
	var stored mountid.Identity
	if err := json.Unmarshal([]byte(commonIdentity), &stored); err != nil {
		t.Fatal(err)
	}
	current, err := mountid.Capture(commonRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !access.MountIdentityMatches(stored, current) {
		t.Fatalf("fixture identity changed: stored=%#v current=%#v", stored, current)
	}
	guard := access.NewGuard(db)
	loaded, err := guard.LoadMountIdentity(context.Background(), "common")
	if err != nil {
		t.Fatalf("fixture load mount: %v", err)
	}
	if err := guard.VerifyMountIdentity(context.Background(), loaded); err != nil {
		t.Fatalf("fixture mount identity %#v: %v", loaded, err)
	}
	if _, err := guard.Authorize(context.Background(), access.CheckRequest{
		Subject:            access.Subject{AccountID: "owner"},
		Locator:            access.Locator{Source: contentref.SourceCommonMount, MountID: "common", Path: "docs/shared.txt"},
		RequiredPermission: "editor",
	}); err != nil {
		t.Fatalf("fixture guard authorization: %v", err)
	}
	if _, err := guard.Authorize(context.Background(), access.CheckRequest{
		Subject:            access.Subject{AccountID: "owner"},
		Locator:            access.Locator{Source: contentref.SourcePersonal, Path: "docs/private.txt"},
		RequiredPermission: "editor",
	}); err != nil {
		t.Fatalf("fixture personal authorization: %v", err)
	}
	return shareFixture{db: db, commonRoot: commonRoot, personalRoot: personalRoot, now: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)}
}

func realDir(t *testing.T, name string) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".membershare-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func identityJSON(t *testing.T, root string) string {
	t.Helper()
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func (f shareFixture) service() *Service {
	return NewService(f.db, access.NewGuard(f.db), WithClock(func() time.Time { return f.now }))
}

func TestCreateUsesAccountMountSourcesAndEnforcesSharePolicy(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	ctx := context.Background()
	common := access.Locator{Source: contentref.SourceCommonMount, MountID: "common", Path: "docs/shared.txt"}
	issued, err := s.Create(ctx, access.Subject{AccountID: "owner"}, CreateRequest{Locator: common})
	if err != nil {
		t.Fatalf("Create(common) error = %v", err)
	}
	if issued.Source != contentref.SourceCommonMount || issued.MountID != "common" || issued.RelativePath != "docs/shared.txt" {
		t.Fatalf("issued common share = %#v", issued.Share)
	}
	if _, err := s.Create(ctx, access.Subject{AccountID: "viewer"}, CreateRequest{Locator: common}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer Create() error = %v, want forbidden", err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET share_enabled=0 WHERE id='common'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, access.Subject{AccountID: "owner"}, CreateRequest{Locator: common}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled policy Create() error = %v, want forbidden", err)
	}
	if _, err := share.NewService(f.db, share.WithClock(func() time.Time { return f.now })).Exchange(ctx, share.ExchangeRequest{PublicID: issued.PublicID, FragmentSecret: issued.Secret}); err == nil {
		t.Fatal("share policy disable did not invalidate public exchange")
	}
}

func TestPersonalShareStoresStoragePathButDTOHidesAccountPrefixAndDefaultMount(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	issued, err := s.Create(context.Background(), access.Subject{AccountID: "owner"}, CreateRequest{Locator: access.Locator{Source: contentref.SourcePersonal, Path: "docs/private.txt"}})
	if err != nil {
		t.Fatalf("Create(personal) error = %v", err)
	}
	if issued.Source != contentref.SourcePersonal || issued.MountID != "" || issued.RelativePath != "docs/private.txt" {
		t.Fatalf("personal DTO leaked storage identity: %#v", issued.Share)
	}
	var mountID, storedPath string
	if err := f.db.QueryRow(`SELECT mount_id,relative_path FROM shares WHERE id=?`, issued.ID).Scan(&mountID, &storedPath); err != nil {
		t.Fatal(err)
	}
	if mountID != "personal-default" || storedPath != "owner/docs/private.txt" {
		t.Fatalf("stored target = %q/%q", mountID, storedPath)
	}
	items, err := s.List(context.Background(), access.Subject{AccountID: "owner"}, ListFilter{})
	if err != nil || len(items) != 1 || items[0].MountID != "" || items[0].RelativePath != "docs/private.txt" {
		t.Fatalf("List() = %#v, %v", items, err)
	}
}

func TestRevokedGrantAndAccountDisableInvalidatePublicShare(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM mount_grants WHERE mount_id='common' AND account_id='owner'`,
		`UPDATE accounts SET status='disabled' WHERE id='owner'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			f := newShareFixture(t)
			issued, err := f.service().Create(context.Background(), access.Subject{AccountID: "owner"}, CreateRequest{Locator: access.Locator{Source: contentref.SourceCommonMount, MountID: "common", Path: "docs/shared.txt"}})
			if err != nil {
				t.Fatal(err)
			}
			public := share.NewService(f.db, share.WithClock(func() time.Time { return f.now }))
			session, err := public.Exchange(context.Background(), share.ExchangeRequest{PublicID: issued.PublicID, FragmentSecret: issued.Secret})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := public.Exchange(context.Background(), share.ExchangeRequest{PublicID: issued.PublicID, FragmentSecret: issued.Secret}); err == nil {
				t.Fatal("stale public share remained usable")
			}
			if _, err := public.VerifySession(context.Background(), session.SessionToken); err == nil {
				t.Fatal("existing share session survived live authorization loss")
			}
		})
	}
}

func TestRevokePathTxUsesMountAndStorageRelativePath(t *testing.T) {
	f := newShareFixture(t)
	s := f.service()
	for _, target := range []string{"docs/shared.txt", "docs/child.txt"} {
		if target == "docs/child.txt" {
			if err := os.WriteFile(filepath.Join(f.commonRoot, target), []byte("child"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Create(context.Background(), access.Subject{AccountID: "owner"}, CreateRequest{Locator: access.Locator{Source: contentref.SourceCommonMount, MountID: "common", Path: target}}); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePathTx(context.Background(), tx, "common", "docs"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM shares WHERE revoked_at IS NOT NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("revoked count = %d, error = %v", count, err)
	}
}
