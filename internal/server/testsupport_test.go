package server

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"omnora/internal/mountid"
	"omnora/internal/store"
)

// createTestSpaceAndMount creates a shared space with ownerAccountID as
// manager, plus an active mount rooted at a fresh temp directory whose
// identity has already been captured and stored so verifyLoadedMountIdentity
// succeeds immediately.
//
// The temp directory is created under /private/tmp (rather than via
// t.TempDir, which on macOS resolves under a symlinked /var) because
// mountid.Capture rejects mount roots that resolve through a symlink
// component. Keeping it outside the source tree also lets the tests run from
// a read-only checkout.
func createTestSpaceAndMount(t *testing.T, db *store.DB, spaceID, mountID, ownerAccountID, mode string) string {
	t.Helper()
	ctx := context.Background()
	root, err := os.MkdirTemp("/private/tmp", "omnora-control-plane-test-")
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
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES (?, 'shared', 'Test Space', ?, 'active')
`, spaceID, ownerAccountID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO space_members(space_id, account_id, permission)
VALUES (?, ?, 'manager')
`, spaceID, ownerAccountID); err != nil {
		t.Fatalf("insert space member: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, 'Docs', ?, 'external', ?, 0, 'active', ?)
`, mountID, spaceID, root, mode, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	return root
}

func addSpaceMember(t *testing.T, db *store.DB, spaceID, accountID, permission string) {
	t.Helper()
	_, err := db.SQL().ExecContext(context.Background(), `
INSERT INTO space_members(space_id, account_id, permission)
VALUES (?, ?, ?)
ON CONFLICT(space_id, account_id) DO UPDATE SET permission = excluded.permission
`, spaceID, accountID, permission)
	if err != nil {
		t.Fatalf("add space member: %v", err)
	}
}
