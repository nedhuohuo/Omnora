package access

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"omnora/internal/domain"

	_ "modernc.org/sqlite"
)

func TestMountServiceIntersectsSpaceMountAndGrantAccess(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL);
CREATE TABLE spaces (id TEXT PRIMARY KEY, status TEXT NOT NULL);
CREATE TABLE space_members (space_id TEXT, account_id TEXT, permission TEXT);
CREATE TABLE mounts (id TEXT PRIMARY KEY, space_id TEXT, display_name TEXT, mode TEXT, status TEXT, allow_public_shares INTEGER);
CREATE TABLE mount_account_grants (mount_id TEXT, account_id TEXT, permission TEXT);
INSERT INTO accounts VALUES ('viewer', 'active'), ('editor', 'active'), ('manager', 'active');
INSERT INTO spaces VALUES ('space', 'active');
INSERT INTO space_members VALUES ('space', 'viewer', 'viewer'), ('space', 'editor', 'editor'), ('space', 'manager', 'manager');
INSERT INTO mounts VALUES ('mount', 'space', 'Docs', 'read_write', 'active', 1);
INSERT INTO mount_account_grants VALUES ('mount', 'viewer', 'manager'), ('mount', 'editor', 'viewer'), ('mount', 'manager', 'manager');
`); err != nil {
		t.Fatal(err)
	}

	service := NewMountService(db)
	for _, tc := range []struct {
		name      string
		accountID string
		operation Operation
		want      bool
	}{
		{name: "viewer reads", accountID: "viewer", operation: OperationRead, want: true},
		{name: "viewer cannot write despite grant", accountID: "viewer", operation: OperationWrite, want: false},
		{name: "editor cannot write through read-only grant", accountID: "editor", operation: OperationWrite, want: false},
		{name: "manager writes", accountID: "manager", operation: OperationWrite, want: true},
		{name: "manager creates public share", accountID: "manager", operation: OperationManageShare, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := service.Authorize(context.Background(), tc.accountID, "space", "mount", tc.operation)
			if tc.want && err != nil {
				t.Fatalf("Authorize() error = %v", err)
			}
			if !tc.want && !errors.Is(err, ErrMountAccessDenied) {
				t.Fatalf("Authorize() error = %v, want denied", err)
			}
			if tc.want && decision.MountMode != domain.MountModeReadWrite {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}

	if _, err := db.Exec(`UPDATE mounts SET allow_public_shares = 0 WHERE id = 'mount'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authorize(context.Background(), "manager", "space", "mount", OperationManageShare); !errors.Is(err, ErrMountAccessDenied) {
		t.Fatalf("Authorize() error = %v, want public share denial", err)
	}
}

func TestManageACLUsesEffectivePermission(t *testing.T) {
	decision := MountDecision{
		SpacePermission:     domain.SpacePermissionManager,
		GrantPermission:     domain.SpacePermissionViewer,
		EffectivePermission: domain.SpacePermissionViewer,
		MountMode:           domain.MountModeReadWrite,
	}
	if allowsMountDecision(decision, OperationManageACL) {
		t.Fatal("viewer-capped manager must not manage mount ACL")
	}
}
