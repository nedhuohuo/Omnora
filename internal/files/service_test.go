package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/domain"
	"omnora/internal/storage"
)

func TestListDirectoryReturnsStableMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.md"), "hello")
	writeFile(t, filepath.Join(root, "photo.JPG"), "image")
	writeFile(t, filepath.Join(root, "report.pdf"), "pdf")
	writeFile(t, filepath.Join(root, "movie.mp4"), "media")
	writeFile(t, filepath.Join(root, "sheet.xlsx"), "office")
	writeFile(t, filepath.Join(root, "archive.bin"), "unknown")
	mkdir(t, filepath.Join(root, "docs"))

	listing, err := NewService().ListDirectory(Mount{
		Root: root,
		Mode: domain.MountModeReadOnly,
	}, ".")
	if err != nil {
		t.Fatalf("ListDirectory() error = %v", err)
	}

	if listing.RelativePath != "." {
		t.Fatalf("RelativePath = %q, want %q", listing.RelativePath, ".")
	}
	if !listing.ReadOnly {
		t.Fatalf("ReadOnly = false, want true")
	}

	want := []struct {
		name        string
		relative    string
		kind        EntryKind
		readOnly    bool
		previewKind PreviewKind
	}{
		{"docs", "docs", EntryKindDir, true, PreviewKindUnknownDownload},
		{"archive.bin", "archive.bin", EntryKindFile, true, PreviewKindUnknownDownload},
		{"movie.mp4", "movie.mp4", EntryKindFile, true, PreviewKindMedia},
		{"notes.md", "notes.md", EntryKindFile, true, PreviewKindMarkdown},
		{"photo.JPG", "photo.JPG", EntryKindFile, true, PreviewKindImage},
		{"report.pdf", "report.pdf", EntryKindFile, true, PreviewKindPDF},
		{"sheet.xlsx", "sheet.xlsx", EntryKindFile, true, PreviewKindOfficeDownload},
	}

	if len(listing.Entries) != len(want) {
		t.Fatalf("len(Entries) = %d, want %d: %#v", len(listing.Entries), len(want), listing.Entries)
	}
	for i, wantEntry := range want {
		got := listing.Entries[i]
		if got.Name != wantEntry.name ||
			got.RelativePath != wantEntry.relative ||
			got.Kind != wantEntry.kind ||
			got.ReadOnly != wantEntry.readOnly ||
			got.PreviewKind != wantEntry.previewKind {
			t.Fatalf("Entries[%d] = %#v, want %#v", i, got, wantEntry)
		}
		if got.ModifiedAt.Location() != time.UTC {
			t.Fatalf("Entries[%d].ModifiedAt location = %v, want UTC", i, got.ModifiedAt.Location())
		}
	}
}

func TestListDirectoryNestedPathAndReadWriteMount(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "docs"))
	writeFile(t, filepath.Join(root, "docs", "readme.txt"), "hello")

	listing, err := NewService().ListDirectory(Mount{
		Root: root,
		Mode: domain.MountModeReadWrite,
	}, "docs/../docs")
	if err != nil {
		t.Fatalf("ListDirectory() error = %v", err)
	}

	if listing.RelativePath != "docs" {
		t.Fatalf("RelativePath = %q, want %q", listing.RelativePath, "docs")
	}
	if listing.ReadOnly {
		t.Fatalf("ReadOnly = true, want false")
	}
	if len(listing.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(listing.Entries))
	}
	got := listing.Entries[0]
	if got.RelativePath != "docs/readme.txt" {
		t.Fatalf("entry RelativePath = %q, want %q", got.RelativePath, "docs/readme.txt")
	}
	if got.ReadOnly {
		t.Fatalf("entry ReadOnly = true, want false")
	}
	if got.Size != int64(len("hello")) {
		t.Fatalf("entry Size = %d, want %d", got.Size, len("hello"))
	}
}

func TestListDirectoryRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	tests := []string{
		"../outside",
		"/absolute",
		storage.ReservedNamespace,
		storage.ReservedNamespace + "/trash",
	}

	for _, relativePath := range tests {
		t.Run(relativePath, func(t *testing.T) {
			_, err := NewService().ListDirectory(Mount{
				Root: root,
				Mode: domain.MountModeReadOnly,
			}, relativePath)
			if err == nil {
				t.Fatalf("ListDirectory() error = nil, want error")
			}
		})
	}
}

func TestListDirectorySkipsReservedNamespaceAndSymlinks(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, storage.ReservedNamespace))
	writeFile(t, filepath.Join(root, "visible.txt"), "hello")
	writeFile(t, filepath.Join(root, "target.txt"), "secret")

	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	listing, err := NewService().ListDirectory(Mount{
		Root: root,
		Mode: domain.MountModeReadOnly,
	}, ".")
	if err != nil {
		t.Fatalf("ListDirectory() error = %v", err)
	}

	for _, entry := range listing.Entries {
		if entry.Name == storage.ReservedNamespace {
			t.Fatalf("reserved namespace entry was listed: %#v", entry)
		}
		if entry.Name == "linked.txt" {
			t.Fatalf("symlink entry was listed: %#v", entry)
		}
	}
}

func TestListDirectoryRejectsNonDirectoryAndInvalidMount(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file.txt"), "hello")

	_, err := NewService().ListDirectory(Mount{
		Root: root,
		Mode: domain.MountModeReadOnly,
	}, "file.txt")
	if !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("ListDirectory() error = %v, want ErrNotDirectory", err)
	}

	_, err = NewService().ListDirectory(Mount{
		Root: root,
		Mode: domain.MountMode("surprise"),
	}, ".")
	if !errors.Is(err, ErrInvalidMountMode) {
		t.Fatalf("ListDirectory() error = %v, want ErrInvalidMountMode", err)
	}

	_, err = NewService().ListDirectory(Mount{
		Mode: domain.MountModeReadOnly,
	}, ".")
	if !errors.Is(err, ErrInvalidMount) {
		t.Fatalf("ListDirectory() error = %v, want ErrInvalidMount", err)
	}
}

func TestStatReturnsMetadataAndRejectsUnsafeObjects(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "docs"))
	writeFile(t, filepath.Join(root, "docs", "readme.txt"), "hello")
	writeFile(t, filepath.Join(root, "target.txt"), "secret")
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mount := Mount{Root: root, Mode: domain.MountModeReadOnly}

	entry, err := NewService().Stat(mount, "docs/readme.txt")
	if err != nil {
		t.Fatalf("Stat(file) error = %v", err)
	}
	if entry.Name != "readme.txt" || entry.RelativePath != "docs/readme.txt" || entry.Kind != EntryKindFile || entry.Size != 5 || !entry.ReadOnly {
		t.Fatalf("Stat(file) = %#v", entry)
	}
	directory, err := NewService().Stat(mount, ".")
	if err != nil || directory.Kind != EntryKindDir || directory.RelativePath != "." {
		t.Fatalf("Stat(root) = %#v, error = %v", directory, err)
	}

	for _, unsafe := range []string{"../secret", "/absolute", ".omnora", "linked.txt"} {
		t.Run(unsafe, func(t *testing.T) {
			_, err := NewService().Stat(mount, unsafe)
			if err == nil {
				t.Fatalf("Stat(%q) error = nil", unsafe)
			}
			if unsafe == "linked.txt" && !errors.Is(err, ErrSymlinkPath) {
				t.Fatalf("Stat(%q) error = %v, want ErrSymlinkPath", unsafe, err)
			}
		})
	}
}

func TestRenameNeverOverwritesExistingFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "source.txt"), "source")
	writeFile(t, filepath.Join(root, "target.txt"), "target")
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite}
	if _, err := NewService().Rename(mount, "source.txt", "target.txt"); err == nil {
		t.Fatal("Rename() unexpectedly overwrote an existing target")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "source.txt")); string(got) != "source" {
		t.Fatalf("source changed to %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "target.txt")); string(got) != "target" {
		t.Fatalf("target changed to %q", got)
	}
}

func TestMountRootRejectsIntermediateSymbolicLinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite}

	if _, err := NewService().ListDirectory(mount, "linked"); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("ListDirectory() error = %v, want ErrSymlinkPath", err)
	}
	if _, _, err := NewService().OpenFile(mount, "linked/secret.txt"); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("OpenFile() error = %v, want ErrSymlinkPath", err)
	}
	if err := NewService().ValidateWritableTarget(mount, "linked/new.txt"); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("ValidateWritableTarget() error = %v, want ErrSymlinkPath", err)
	}
	if _, err := NewService().ValidateShareTarget(mount, "linked/secret.txt"); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("ValidateShareTarget() error = %v, want ErrSymlinkPath", err)
	}
}

func TestCreateDirectoryUsesVerifiedMountRoot(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "docs"))
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite}

	created, err := NewService().CreateDirectory(mount, "docs", "drafts")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	if created != "docs/drafts" {
		t.Fatalf("created = %q, want docs/drafts", created)
	}
	info, err := os.Stat(filepath.Join(root, "docs", "drafts"))
	if err != nil || !info.IsDir() {
		t.Fatalf("created directory stat = %v, info = %#v", err, info)
	}

	_, err = NewService().CreateDirectory(Mount{Root: root, Mode: domain.MountModeReadOnly}, ".", "blocked")
	if !errors.Is(err, ErrInvalidMountMode) {
		t.Fatalf("read-only CreateDirectory() error = %v, want ErrInvalidMountMode", err)
	}
	if _, err = NewService().CreateDirectory(mount, ".", storage.ReservedNamespace); err == nil {
		t.Fatal("CreateDirectory() allowed reserved namespace")
	}
}

func writeFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", name, err)
	}
}

func mkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", name, err)
	}
}
