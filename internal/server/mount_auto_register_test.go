package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func TestSelectConfiguredMountsKeepsShallowIndependentMountPoints(t *testing.T) {
	externalRoot := filepath.Join(string(filepath.Separator), "mnt", "omnora")
	managedRoot := filepath.Join(string(filepath.Separator), "srv", "omnora", "managed")
	slot1 := filepath.Join(externalRoot, "slot1")
	slot2 := filepath.Join(externalRoot, "slot2")
	entries := []mountid.MountInfo{
		{Available: true, Point: string(filepath.Separator)},
		{Available: true, Point: filepath.Join(string(filepath.Separator), "var", "lib", "omnora")},
		{Available: true, Point: externalRoot},
		{Available: true, Point: slot2, ReadOnly: true},
		{Available: true, Point: filepath.Join(slot1, "nested")},
		{Available: true, Point: slot1},
		{Available: true, Point: managedRoot},
	}

	got := selectConfiguredMounts(entries, managedRoot, externalRoot)
	if len(got) != 3 {
		t.Fatalf("selected mounts = %#v, want 3", got)
	}
	if got[0].Point != slot1 || got[1].Point != slot2 || got[2].Point != managedRoot {
		t.Fatalf("selected mounts = %#v", got)
	}
	if !got[1].ReadOnly {
		t.Fatal("slot2 should preserve its read-only mount option")
	}
}

func TestNewServerAutoRegistersDockerMountsIdempotently(t *testing.T) {
	db := openAutoMountTestDB(t)
	initialized := initializeAutoMountTestAdmin(t, db)
	managedRoot, externalRoot, slotRoot := autoMountTestRoots(t)
	discovery := autoMountTestDiscovery(managedRoot, slotRoot)

	srv := NewServer(config.Config{
		Storage: config.StorageConfig{ManagedDir: managedRoot, PredeclaredMountRoot: externalRoot},
		Routes:  map[domain.RouteGroup]bool{},
	}, db, withMountDiscovery(discovery))

	assertAutoMountRows(t, db, initialized.Account.ID, managedRoot, slotRoot)
	registered, err := srv.autoRegisterDockerMounts(context.Background(), "", "")
	if err != nil {
		t.Fatalf("second automatic registration: %v", err)
	}
	if registered != 0 {
		t.Fatalf("second automatic registration added %d mounts, want 0", registered)
	}
	assertAutoMountRows(t, db, initialized.Account.ID, managedRoot, slotRoot)
}

func TestInitializeAutoRegistersDockerMounts(t *testing.T) {
	db := openAutoMountTestDB(t)
	secret, err := identity.New(db.SQL(), identity.Options{}).PrepareInitialization(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("prepare initialization: %v", err)
	}
	managedRoot, externalRoot, slotRoot := autoMountTestRoots(t)
	srv := NewServer(config.Config{
		Storage: config.StorageConfig{ManagedDir: managedRoot, PredeclaredMountRoot: externalRoot},
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
	}, db, withMountDiscovery(autoMountTestDiscovery(managedRoot, slotRoot)))

	payload, err := json.Marshal(map[string]string{
		"token":       secret.Token,
		"email":       "admin@example.test",
		"displayName": "Admin",
		"password":    apiTestPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/initialize", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("initialize status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AutoRegisteredMounts int `json:"autoRegisteredMounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if response.AutoRegisteredMounts != 2 {
		t.Fatalf("autoRegisteredMounts = %d, want 2", response.AutoRegisteredMounts)
	}
	assertAutoMountRows(t, db, response.User.ID, managedRoot, slotRoot)
}

func openAutoMountTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "auto-mount.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func initializeAutoMountTestAdmin(t *testing.T, db *store.DB) identity.AccountWithPersonalSpace {
	t.Helper()
	svc := identity.New(db.SQL(), identity.Options{})
	secret, err := svc.PrepareInitialization(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("prepare initialization: %v", err)
	}
	initialized, err := svc.Initialize(context.Background(), identity.InitializationRequest{
		Token:       secret.Token,
		Email:       "admin@example.test",
		DisplayName: "Admin",
		Password:    apiTestPassword,
	})
	if err != nil {
		t.Fatalf("initialize admin: %v", err)
	}
	return initialized
}

func autoMountTestRoots(t *testing.T) (string, string, string) {
	t.Helper()
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	base, err := os.MkdirTemp(workspace, ".auto-mount-")
	if err != nil {
		t.Fatalf("create mount test root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	managedRoot := filepath.Join(base, "managed")
	externalRoot := filepath.Join(base, "external")
	slotRoot := filepath.Join(externalRoot, "slot1")
	for _, path := range []string{managedRoot, slotRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}
	return managedRoot, externalRoot, slotRoot
}

func autoMountTestDiscovery(managedRoot, slotRoot string) mountDiscoveryFunc {
	return func(string, string) ([]mountid.MountInfo, error) {
		return []mountid.MountInfo{
			{Available: true, Point: managedRoot},
			{Available: true, Point: slotRoot, ReadOnly: true},
		}, nil
	}
}

func assertAutoMountRows(t *testing.T, db *store.DB, accountID, managedRoot, slotRoot string) {
	t.Helper()
	rows, err := db.SQL().QueryContext(context.Background(), `
SELECT m.display_name, m.root_path, m.kind, m.mode, mg.permission
FROM mounts m
JOIN mount_account_grants mg ON mg.mount_id = m.id
WHERE mg.account_id = ? AND m.status = 'active'
ORDER BY m.root_path
`, accountID)
	if err != nil {
		t.Fatalf("query automatic mounts: %v", err)
	}
	defer rows.Close()

	got := make(map[string][4]string)
	for rows.Next() {
		var name, root, kind, mode, permission string
		if err := rows.Scan(&name, &root, &kind, &mode, &permission); err != nil {
			t.Fatalf("scan automatic mount: %v", err)
		}
		got[root] = [4]string{name, kind, mode, permission}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("automatic mounts = %#v, want 2", got)
	}
	if got[managedRoot] != [4]string{"Managed Storage", "managed", "read_write", "manager"} {
		t.Fatalf("managed mount = %#v", got[managedRoot])
	}
	if got[slotRoot] != [4]string{"slot1", "external", "read_only", "manager"} {
		t.Fatalf("external mount = %#v", got[slotRoot])
	}
}
