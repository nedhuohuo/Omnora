package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/config"
	"omnora/internal/domain"
)

func TestEnsurePersonalStorageCreatesOneAccountRootAndMount(t *testing.T) {
	db, _ := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".personal-storage-")
	if err != nil {
		t.Fatalf("create personal root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s := NewServer(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage:           config.StorageConfig{PersonalStorageDir: root},
	}, db)

	if err := s.ensurePersonalStorage(context.Background(), member.ID); err != nil {
		t.Fatalf("ensure personal storage: %v", err)
	}
	if err := s.ensurePersonalStorage(context.Background(), member.ID); err != nil {
		t.Fatalf("repeat ensure personal storage: %v", err)
	}
	path := filepath.Join(root, member.ID)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("personal root = %s, stat = %v", path, err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM mounts WHERE space_id = (SELECT id FROM spaces WHERE kind = 'personal' AND owner_account_id = ?)`, member.ID).Scan(&count); err != nil {
		t.Fatalf("count personal mounts: %v", err)
	}
	if count != 1 {
		t.Fatalf("personal mount count = %d, want 1", count)
	}
}
