package mountid

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureRejectsRelativePath(t *testing.T) {
	_, err := capture("data", missingMountInfoPath(t))
	if !errors.Is(err, ErrIdentityUnverifiable) {
		t.Fatalf("capture() error = %v, want ErrIdentityUnverifiable", err)
	}
}

func TestCaptureRejectsSymlinkComponent(t *testing.T) {
	base := realTempDir(t)
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := capture(link, missingMountInfoPath(t))
	if !errors.Is(err, ErrIdentityUnverifiable) {
		t.Fatalf("capture() error = %v, want ErrIdentityUnverifiable", err)
	}
}

func TestVerifyCandidateRootRejectsPathOverlaps(t *testing.T) {
	base := realTempDir(t)
	parent := filepath.Join(base, "data")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	childIdentity, err := capture(child, missingMountInfoPath(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifyCandidateRoot(parent, []Identity{childIdentity}, missingMountInfoPath(t))
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("verifyCandidateRoot(parent) error = %v, want ErrMountConflict", err)
	}

	parentIdentity, err := capture(parent, missingMountInfoPath(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifyCandidateRoot(child, []Identity{parentIdentity}, missingMountInfoPath(t))
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("verifyCandidateRoot(child) error = %v, want ErrMountConflict", err)
	}
}

func TestVerifyCandidateRootRejectsSameCleanPath(t *testing.T) {
	base := realTempDir(t)
	root := filepath.Join(base, "data")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	identity, err := capture(root, missingMountInfoPath(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifyCandidateRoot(filepath.Join(root, "."), []Identity{identity}, missingMountInfoPath(t))
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("verifyCandidateRoot() error = %v, want ErrMountConflict", err)
	}
}

func TestCheckConflictsRejectsSameDeviceAndInode(t *testing.T) {
	candidate := Identity{Path: "/mnt/a", Device: 10, Inode: 20}
	existing := Identity{Path: "/mnt/b", Device: 10, Inode: 20}

	err := CheckConflicts(candidate, []Identity{existing})
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("CheckConflicts() error = %v, want ErrMountConflict", err)
	}
}

func TestCheckConflictsUsesPathComponentBoundaries(t *testing.T) {
	candidate := Identity{Path: "/data/a", Device: 10, Inode: 20}
	existing := Identity{Path: "/data/ab", Device: 10, Inode: 21}

	err := CheckConflicts(candidate, []Identity{existing})
	if err != nil {
		t.Fatalf("CheckConflicts() error = %v, want nil", err)
	}
}

func TestIsBindMountAtMatchesExactMountPoint(t *testing.T) {
	dir := realTempDir(t)
	mountInfo := filepath.Join(dir, "mountinfo")
	const table = "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
		"2 1 8:2 /exports/photos /mnt/omnora/slot1 rw - ext4 /dev/sda2 rw\n" +
		"3 1 8:3 /exports/music /mnt/omnora/slot2 rw - ext4 /dev/sda3 rw\n"
	if err := os.WriteFile(mountInfo, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/mnt/omnora/slot1", "/mnt/omnora/slot2"} {
		bound, err := isBindMountAt(path, mountInfo)
		if err != nil || !bound {
			t.Fatalf("isBindMountAt(%s) = %v, %v; want true", path, bound, err)
		}
	}
	// A path that is covered by another mount point but has no entry of its
	// own is not a bound slot.
	bound, err := isBindMountAt("/mnt/omnora", mountInfo)
	if err != nil || bound {
		t.Fatalf("isBindMountAt(/mnt/omnora) = %v, %v; want false", bound, err)
	}
	// An entirely absent path is not bound.
	bound, err = isBindMountAt("/mnt/omnora/slot9", mountInfo)
	if err != nil || bound {
		t.Fatalf("isBindMountAt(slot9) = %v, %v; want false", bound, err)
	}
	// A missing mountinfo table means nothing can be detected as bound.
	bound, err = isBindMountAt("/mnt/omnora/slot1", missingMountInfoPath(t))
	if err != nil || bound {
		t.Fatalf("isBindMountAt(missing) = %v, %v; want false", bound, err)
	}
}

func TestParseMountInfoCapturesFieldsAndEscapes(t *testing.T) {
	const sample = "42 30 8:1 /source\\040root /mnt/data\\040one rw,relatime shared:7 - ext4 /dev/sda1 rw\n"

	entries, err := parseMountInfo(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}

	got := entries[0]
	if !got.Available || got.ID != 42 || got.ParentID != 30 || got.Device != "8:1" || got.Root != "/source root" || got.Point != "/mnt/data one" || got.FSType != "ext4" || got.Source != "/dev/sda1" {
		t.Fatalf("parsed mountinfo = %+v", got)
	}
}

func TestBestMountForPathUsesLongestComponentMatch(t *testing.T) {
	entries := []MountInfo{
		{Available: true, ID: 1, Device: "8:1", Root: "/", Point: "/mnt/data", Source: "/dev/sda1"},
		{Available: true, ID: 2, Device: "8:1", Root: "/nested", Point: "/mnt/data/nested", Source: "/dev/sda1"},
		{Available: true, ID: 3, Device: "8:1", Root: "/other", Point: "/mnt/database", Source: "/dev/sda1"},
	}

	got := bestMountForPath("/mnt/data/nested/files", entries)
	if got.ID != 2 {
		t.Fatalf("bestMountForPath() ID = %d, want 2", got.ID)
	}
}

func TestCheckConflictsRejectsBindSourceOverlap(t *testing.T) {
	candidate := Identity{
		Path:   "/mnt/photos",
		Device: 1,
		Inode:  101,
		Mount:  MountInfo{Available: true, ID: 10, Device: "8:1", Root: "/exports/photos", Point: "/mnt/photos", Source: "/dev/sda1"},
	}
	existing := Identity{
		Path:   "/mnt/archive",
		Device: 1,
		Inode:  202,
		Mount:  MountInfo{Available: true, ID: 11, Device: "8:1", Root: "/exports/photos/2026", Point: "/mnt/archive", Source: "/dev/sda1"},
	}

	err := CheckConflicts(candidate, []Identity{existing})
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("CheckConflicts() error = %v, want ErrMountConflict", err)
	}
}

func TestCaptureToleratesAbsentMountInfo(t *testing.T) {
	base := realTempDir(t)
	root := filepath.Join(base, "data")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	identity, err := capture(root, missingMountInfoPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Path != root || identity.Device == 0 || identity.Inode == 0 || identity.Mount.Available {
		t.Fatalf("identity = %+v", identity)
	}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func missingMountInfoPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing-mountinfo")
}
