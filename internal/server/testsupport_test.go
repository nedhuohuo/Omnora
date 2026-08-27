package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/mountid"
	"omnora/internal/store"
)

// createTestMount creates an active common mount with an editor grant
// for ownerAccountID, rooted at a fresh temp directory whose
// identity has already been captured and stored so verifyLoadedMountIdentity
// succeeds immediately.
//
// The temp directory is created under the workspace (rather than via
// t.TempDir, which on macOS resolves under a symlinked /var) because
// mountid.Capture rejects mount roots that resolve through a symlink
// component.
func createTestMount(t *testing.T, db *store.DB, mountID, ownerAccountID, mode string) string {
	t.Helper()
	ctx := context.Background()
	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace dir: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".control-plane-test-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	captured, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture mount identity: %v", err)
	}
	identityJSON, err := json.Marshal(captured)
	if err != nil {
		t.Fatalf("marshal mount identity: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, ?, 'common', 'external', 'normal', ?, 0, 'active', ?)
`, mountID, "Docs "+mountID, root, mode, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mount_grants(mount_id, account_id, permission)
VALUES (?, ?, 'editor')
`, mountID, ownerAccountID); err != nil {
		t.Fatalf("insert mount grant: %v", err)
	}
	return root
}
