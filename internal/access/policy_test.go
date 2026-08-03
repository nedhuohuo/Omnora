package access

import (
	"testing"

	"omnora/internal/domain"
)

func TestDefaultRouteGroupsFailClosed(t *testing.T) {
	groups := DefaultRouteGroups()
	for _, group := range domain.AllRouteGroups {
		if groups.Enabled(group) {
			t.Fatalf("expected %s to be disabled by default", group)
		}
	}
}

func TestAllowsSpacePermissionAndMountModeIntersection(t *testing.T) {
	tests := []struct {
		name       string
		permission domain.SpacePermission
		mountMode  domain.MountMode
		operation  Operation
		want       bool
	}{
		{"viewer can read read-only mount", domain.SpacePermissionViewer, domain.MountModeReadOnly, OperationRead, true},
		{"viewer cannot write read-write mount", domain.SpacePermissionViewer, domain.MountModeReadWrite, OperationWrite, false},
		{"editor can write read-write mount", domain.SpacePermissionEditor, domain.MountModeReadWrite, OperationWrite, true},
		{"manager cannot write read-only mount", domain.SpacePermissionManager, domain.MountModeReadOnly, OperationWrite, false},
		{"manager can manage shares", domain.SpacePermissionManager, domain.MountModeReadOnly, OperationManageShare, true},
		{"editor cannot manage acl", domain.SpacePermissionEditor, domain.MountModeReadWrite, OperationManageACL, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Allows(tt.permission, tt.mountMode, tt.operation); got != tt.want {
				t.Fatalf("Allows() = %v, want %v", got, tt.want)
			}
		})
	}
}
