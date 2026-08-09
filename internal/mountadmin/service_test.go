package mountadmin_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"omnora/internal/domain"
	"omnora/internal/mountadmin"
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
	root := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create external root: %v", err)
	}
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

var _ *sql.DB
