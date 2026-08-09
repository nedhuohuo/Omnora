package mountadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"omnora/internal/domain"
	"omnora/internal/mountadmin"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

type fixture struct {
	db            *store.DB
	service       *mountadmin.Service
	root          string
	initialAdmin  account
	ordinaryAdmin account
	member        account
}

type account struct{ ID, Email string }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "mountadmin.db"), BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".mountadmin-external-")
	if err != nil {
		t.Fatalf("create external root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	accounts := []account{
		{ID: "admin-initial", Email: "initial@example.test"},
		{ID: "admin-ordinary", Email: "ordinary@example.test"},
		{ID: "member", Email: "member@example.test"},
	}
	for _, item := range accounts {
		if _, err := db.SQL().Exec(`INSERT INTO accounts(id, email, display_name, role, status) VALUES (?, ?, ?, ?, 'active')`, item.ID, item.Email, item.Email, map[bool]string{true: "admin", false: "member"}[item.ID != "member"]); err != nil {
			t.Fatalf("insert account %s: %v", item.ID, err)
		}
	}
	if _, err := db.SQL().Exec(`UPDATE system_state SET value = ? WHERE key = 'initial_admin_account_id'`, accounts[0].ID); err != nil {
		// The initial schema does not create this marker until initialization.
		if _, insertErr := db.SQL().Exec(`INSERT INTO system_state(key, value) VALUES ('initial_admin_account_id', ?)`, accounts[0].ID); insertErr != nil {
			t.Fatalf("insert initial admin marker: %v", insertErr)
		}
	}
	return &fixture{
		db:           db,
		service:      mountadmin.New(db.SQL(), root),
		root:         root,
		initialAdmin: accounts[0], ordinaryAdmin: accounts[1], member: accounts[2],
	}
}

func (f *fixture) insertMount(t *testing.T, id, name string, governance domain.MountGovernance) {
	t.Helper()
	root := filepath.Join(f.root, id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	if _, err := f.db.SQL().Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, share_enabled, status)
VALUES (?, ?, ?, 'common', 'external', ?, 'read_only', 0, 1, 'active')
`, id, name, root, governance); err != nil {
		t.Fatalf("insert mount %s: %v", id, err)
	}
}

func mountIDs(items []mountadmin.Mount) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestListMountsFiltersPersonalAndRestrictedMounts(t *testing.T) {
	fixture := newFixture(t)
	fixture.insertMount(t, "normal", "Normal", domain.MountGovernanceNormal)
	fixture.insertMount(t, "restricted", "Restricted", domain.MountGovernanceRestricted)

	ordinary, err := fixture.service.ListMounts(context.Background(), fixture.ordinaryAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := mountIDs(ordinary); !slices.Equal(got, []string{"normal"}) {
		t.Fatalf("ordinary mounts = %v", got)
	}

	initial, err := fixture.service.ListMounts(context.Background(), fixture.initialAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := mountIDs(initial); !slices.Equal(got, []string{"normal", "restricted"}) {
		t.Fatalf("initial mounts = %v", got)
	}
}

func TestLoadGovernableMountHidesRestrictedFromOrdinaryAdmin(t *testing.T) {
	fixture := newFixture(t)
	fixture.insertMount(t, "restricted", "Restricted", domain.MountGovernanceRestricted)

	_, err := fixture.service.LoadMount(context.Background(), fixture.ordinaryAdmin.ID, "restricted")
	if !errors.Is(err, mountadmin.ErrNotFound) {
		t.Fatalf("LoadMount error = %v, want ErrNotFound", err)
	}
}

func TestListMountsDoesNotReturnPersonalDefault(t *testing.T) {
	fixture := newFixture(t)
	items, err := fixture.service.ListMounts(context.Background(), fixture.initialAdmin.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "personal-default" || item.Governance == domain.MountGovernanceSystem {
			t.Fatalf("personal default leaked: %#v", item)
		}
	}
}

func TestCreateMountAllowsZeroGrantsWithoutImplicitAdminAccess(t *testing.T) {
	fixture := newFixture(t)
	root := filepath.Join(fixture.root, "photos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create mount root: %v", err)
	}

	created, err := fixture.service.CreateMount(context.Background(), fixture.ordinaryAdmin.ID, mountadmin.CreateRequest{
		ID: "mnt-photos", DisplayName: "Photos", RootPath: root,
		Governance:   domain.MountGovernanceNormal,
		Mode:         domain.MountModeReadOnly,
		IndexEnabled: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.GrantCount != 0 {
		t.Fatalf("grant count = %d, want 0", created.GrantCount)
	}
	var grants int
	if err := fixture.db.SQL().QueryRow(`SELECT COUNT(*) FROM mount_grants WHERE mount_id = 'mnt-photos'`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("grant count in database = %d, want 0", grants)
	}
}

func TestCreateMountStoresExplicitViewerAndEditorGrants(t *testing.T) {
	fixture := newFixture(t)
	root := filepath.Join(fixture.root, "shared")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create mount root: %v", err)
	}

	created, err := fixture.service.CreateMount(context.Background(), fixture.initialAdmin.ID, mountadmin.CreateRequest{
		ID: "mnt-shared", DisplayName: "Shared", RootPath: root,
		Governance: domain.MountGovernanceRestricted,
		Mode:       domain.MountModeReadOnly,
		Grants: []mountadmin.GrantInput{
			{AccountID: fixture.member.ID, Permission: domain.ContentPermissionViewer},
			{AccountID: fixture.ordinaryAdmin.ID, Permission: domain.ContentPermissionEditor},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.GrantCount != 2 {
		t.Fatalf("grant count = %d, want 2", created.GrantCount)
	}
	grants, err := fixture.service.ListGrants(context.Background(), fixture.initialAdmin.ID, "mnt-shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %#v", grants)
	}
	permissions := map[string]domain.ContentPermission{}
	for _, grant := range grants {
		permissions[grant.AccountID] = grant.Permission
	}
	if permissions[fixture.member.ID] != domain.ContentPermissionViewer || permissions[fixture.ordinaryAdmin.ID] != domain.ContentPermissionEditor {
		t.Fatalf("grant permissions = %#v", permissions)
	}
}

func TestOrdinaryAdminCannotCreateRestrictedMount(t *testing.T) {
	fixture := newFixture(t)
	root := filepath.Join(fixture.root, "secret")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	_, err := fixture.service.CreateMount(context.Background(), fixture.ordinaryAdmin.ID, mountadmin.CreateRequest{
		ID: "mnt-secret", DisplayName: "Secret", RootPath: root,
		Governance: domain.MountGovernanceRestricted,
		Mode:       domain.MountModeReadOnly,
	}, nil)
	if !errors.Is(err, mountadmin.ErrRestrictedGovernance) {
		t.Fatalf("error = %v", err)
	}
}

func TestGrantMutationsOnlyAcceptViewerOrEditor(t *testing.T) {
	fixture := newFixture(t)
	fixture.insertMount(t, "normal", "Normal", domain.MountGovernanceNormal)
	_, err := fixture.service.PutGrant(context.Background(), fixture.ordinaryAdmin.ID, "normal", fixture.member.ID, domain.ContentPermission("manager"), nil)
	if !errors.Is(err, mountadmin.ErrInvalidInput) {
		t.Fatalf("manager grant error = %v", err)
	}
}

func TestDeleteMountRequiresExactDisplayName(t *testing.T) {
	fixture := newFixture(t)
	fixture.insertMount(t, "normal", "Normal", domain.MountGovernanceNormal)
	_, err := fixture.service.DeleteMount(context.Background(), fixture.ordinaryAdmin.ID, "normal", "normal", nil)
	if !errors.Is(err, mountadmin.ErrConfirmationRequired) {
		t.Fatalf("error = %v", err)
	}
}

func TestDeleteMountKeepsFilesAndRevokesDerivedState(t *testing.T) {
	fixture := newFixture(t)
	root := filepath.Join(fixture.root, "archive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write physical file: %v", err)
	}
	identityJSON := captureIdentityJSON(t, root)
	if _, err := fixture.db.SQL().Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, share_enabled, status, mount_identity_json)
VALUES ('archive', 'Archive', ?, 'common', 'external', 'normal', 'read_only', 1, 1, 'active', ?)
`, root, identityJSON); err != nil {
		t.Fatalf("insert archive mount: %v", err)
	}
	if _, err := fixture.db.SQL().Exec(`INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('archive', ?, 'viewer')`, fixture.member.ID); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
	if _, err := fixture.db.SQL().Exec(`INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, expires_at) VALUES ('token-1', 'public-1', 'hash', ?, 'Token', '[]', '2099-01-01T00:00:00Z')`, fixture.member.ID); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	if _, err := fixture.db.SQL().Exec(`INSERT INTO ai_token_boundaries(token_id, source, mount_id, relative_path) VALUES ('token-1', 'common_mount', 'archive', '')`); err != nil {
		t.Fatalf("insert token boundary: %v", err)
	}

	result, err := fixture.service.DeleteMount(context.Background(), fixture.initialAdmin.ID, "archive", "Archive", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Deleted || result.DeleteData || result.DataDeleted {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("physical file was removed: %v", err)
	}
	assertCount(t, fixture.db, `SELECT COUNT(*) FROM mount_grants WHERE mount_id = 'archive'`, 0)
	assertCount(t, fixture.db, `SELECT COUNT(*) FROM ai_token_boundaries WHERE mount_id = 'archive'`, 0)
	var status string
	if err := fixture.db.SQL().QueryRow(`SELECT status FROM mounts WHERE id = 'archive'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" {
		t.Fatalf("status = %q", status)
	}
}

func TestReverifyMountRestoresActiveStatusWithFreshIdentity(t *testing.T) {
	fixture := newFixture(t)
	root := filepath.Join(fixture.root, "reverify")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	identityJSON := captureIdentityJSON(t, root)
	if _, err := fixture.db.SQL().Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, share_enabled, status, mount_identity_json)
VALUES ('reverify', 'Reverify', ?, 'common', 'external', 'normal', 'read_only', 0, 1, 'unavailable', ?)
`, root, identityJSON); err != nil {
		t.Fatalf("insert reverify mount: %v", err)
	}
	updated, err := fixture.service.ReverifyMount(context.Background(), fixture.ordinaryAdmin.ID, "reverify", nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "active" {
		t.Fatalf("status = %q, want active", updated.Status)
	}
}

func captureIdentityJSON(t *testing.T, root string) string {
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

func assertCount(t *testing.T, db *store.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.SQL().QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
}
