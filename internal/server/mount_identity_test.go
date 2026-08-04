package server

import (
	"testing"

	"omnora/internal/mountid"
)

func TestMountIdentityIgnoresEphemeralMountIDs(t *testing.T) {
	stored := mountid.Identity{
		Path:   "/mnt/omnora",
		Device: 10,
		Inode:  20,
		Statx:  mountid.StatxInfo{Available: true, DeviceMajor: 252, DeviceMinor: 3, Inode: 20, MountID: 100},
		Mount:  mountid.MountInfo{Available: true, ID: 1, Device: "252:3", Root: "/data", Point: "/mnt/omnora", FSType: "ext4", Source: "/dev/vda1"},
	}
	current := stored
	current.Statx.MountID = 999
	current.Mount.ID = 42
	if !mountIdentityMatches(stored, current) {
		t.Fatal("expected durable identity to match across remount IDs")
	}
	if !mountIdentityNeedsRefresh(stored, current) {
		t.Fatal("expected ephemeral mount id change to refresh stored identity")
	}
	current.Inode = 21
	if mountIdentityMatches(stored, current) {
		t.Fatal("expected inode change to fail identity match")
	}
}
