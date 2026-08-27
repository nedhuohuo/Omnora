package files

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/access"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/mountid"
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

// verifiedAuthorizedMount builds a common-mount AuthorizedMount whose identity
// JSON is freshly captured from root, so the rooted-open identity check passes
// for that root. storageRelativePath is used for both the host-visible
// RelativePath and the StorageRelativePath, which is the common-mount layout.
func verifiedAuthorizedMount(t *testing.T, root, storageRelativePath string) access.AuthorizedMount {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root) error = %v", err)
	}
	identity, err := mountid.Capture(resolved)
	if err != nil {
		t.Fatalf("Capture(root) error = %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal(identity) error = %v", err)
	}
	return access.AuthorizedMount{
		Source:              contentref.SourceCommonMount,
		ID:                  "verified-mount",
		Root:                resolved,
		MountRoot:           resolved,
		StorageRelativePath: storageRelativePath,
		RelativePath:        storageRelativePath,
		IdentityJSON:        string(identityJSON),
	}
}

func TestOpenVerifiedRegularFileOpensAuthorizedObject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "hello")
	mount := verifiedAuthorizedMount(t, root, "notes.txt")

	leaf, info, err := OpenVerifiedRegularFile(mount)
	if err != nil {
		t.Fatalf("OpenVerifiedRegularFile() error = %v", err)
	}
	defer leaf.Close()
	if !info.Mode().IsRegular() || info.Size() != int64(len("hello")) {
		t.Fatalf("Stat = %#v, want regular file of size 5", info)
	}
	contents, err := io.ReadAll(leaf)
	if err != nil {
		t.Fatalf("ReadAll(leaf) error = %v", err)
	}
	if string(contents) != "hello" {
		t.Fatalf("leaf contents = %q, want %q", contents, "hello")
	}
}

// The leaf itself must not be a symlink, even one pointing outside the root:
// the probe content lives outside and must never be readable.
func TestOpenVerifiedRegularFileRejectsExternalSymlinkLeaf(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "probe.txt"), "probe-secret")
	if err := os.Symlink(filepath.Join(outside, "probe.txt"), filepath.Join(root, "swapped")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mount := verifiedAuthorizedMount(t, root, "swapped")

	leaf, _, err := OpenVerifiedRegularFile(mount)
	if leaf != nil {
		leaf.Close()
	}
	if !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrSymlinkPath", err)
	}
}

func TestOpenVerifiedRegularFileRejectsIntermediateExternalSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "probe.txt"), "probe-secret")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mount := verifiedAuthorizedMount(t, root, "linked/probe.txt")

	leaf, _, err := OpenVerifiedRegularFile(mount)
	if leaf != nil {
		leaf.Close()
	}
	if !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrSymlinkPath", err)
	}
}

// os.Root follows symlinks inside the root, so an in-root symlink to another
// authorized file must still be rejected: the mount policy forbids symlink
// objects regardless of where they point.
func TestOpenVerifiedRegularFileRejectsInRootSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real.txt"), "real")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "swapped")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mount := verifiedAuthorizedMount(t, root, "swapped")

	leaf, _, err := OpenVerifiedRegularFile(mount)
	if leaf != nil {
		leaf.Close()
	}
	if !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrSymlinkPath", err)
	}
}

// The mount root identity is captured at authorization time; replacing the root
// directory (or supplying an identity that does not match the open root) must
// fail before any object can be opened.
func TestOpenVerifiedRegularFileRejectsRootIdentityMismatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "hello")
	mount := verifiedAuthorizedMount(t, root, "notes.txt")

	t.Run("wrong identity json", func(t *testing.T) {
		other, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("EvalSymlinks(other) error = %v", err)
		}
		otherIdentity, err := mountid.Capture(other)
		if err != nil {
			t.Fatalf("Capture(other) error = %v", err)
		}
		otherJSON, err := json.Marshal(otherIdentity)
		if err != nil {
			t.Fatalf("Marshal(otherIdentity) error = %v", err)
		}
		mismatch := mount
		mismatch.IdentityJSON = string(otherJSON)
		leaf, _, err := OpenVerifiedRegularFile(mismatch)
		if leaf != nil {
			leaf.Close()
		}
		if !errors.Is(err, access.ErrMountIdentityUnverifiable) {
			t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrMountIdentityUnverifiable", err)
		}
	})

	t.Run("empty identity json", func(t *testing.T) {
		missing := mount
		missing.IdentityJSON = ""
		leaf, _, err := OpenVerifiedRegularFile(missing)
		if leaf != nil {
			leaf.Close()
		}
		if !errors.Is(err, access.ErrMountIdentityUnverifiable) {
			t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrMountIdentityUnverifiable", err)
		}
	})

	t.Run("directory replacement", func(t *testing.T) {
		parent := t.TempDir()
		rootPath := filepath.Join(parent, "mount")
		replacementPath := filepath.Join(parent, "replacement")
		if err := os.Mkdir(rootPath, 0o755); err != nil {
			t.Fatalf("Mkdir(root) error = %v", err)
		}
		writeFile(t, filepath.Join(rootPath, "notes.txt"), "hello")
		original := verifiedAuthorizedMount(t, rootPath, "notes.txt")
		if err := os.Mkdir(replacementPath, 0o755); err != nil {
			t.Fatalf("Mkdir(replacement) error = %v", err)
		}
		writeFile(t, filepath.Join(replacementPath, "notes.txt"), "replacement")
		if err := os.Rename(rootPath, filepath.Join(parent, "mount-old")); err != nil {
			t.Fatalf("Rename(root) error = %v", err)
		}
		if err := os.Rename(replacementPath, rootPath); err != nil {
			t.Fatalf("Rename(replacement) error = %v", err)
		}
		leaf, _, err := OpenVerifiedRegularFile(original)
		if leaf != nil {
			leaf.Close()
		}
		if !errors.Is(err, access.ErrMountIdentityUnverifiable) {
			t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrMountIdentityUnverifiable", err)
		}
	})

	t.Run("empty mount root", func(t *testing.T) {
		empty := mount
		empty.MountRoot = ""
		leaf, _, err := OpenVerifiedRegularFile(empty)
		if leaf != nil {
			leaf.Close()
		}
		if !errors.Is(err, ErrInvalidMount) {
			t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrInvalidMount", err)
		}
	})
}

// Replacing the authorized object with a directory must be rejected as a
// non-regular file, not served as an empty object.
func TestOpenVerifiedRegularFileRejectsNonRegularFile(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "notes.txt"))
	mount := verifiedAuthorizedMount(t, root, "notes.txt")

	leaf, _, err := OpenVerifiedRegularFile(mount)
	if leaf != nil {
		leaf.Close()
	}
	if !errors.Is(err, ErrNotFile) {
		t.Fatalf("OpenVerifiedRegularFile() error = %v, want ErrNotFile", err)
	}
}

// The storage-relative path is cleaned before open, so traversal, absolute
// paths and the reserved namespace are rejected the same way as every other
// mount open.
func TestOpenVerifiedRegularFileRejectsUnsafeStoragePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "hello")
	for _, storagePath := range []string{"..", "../outside", "/absolute", storage.ReservedNamespace, storage.ReservedNamespace + "/tmp"} {
		t.Run(storagePath, func(t *testing.T) {
			mount := verifiedAuthorizedMount(t, root, storagePath)
			leaf, _, err := OpenVerifiedRegularFile(mount)
			if leaf != nil {
				leaf.Close()
			}
			if err == nil {
				t.Fatalf("OpenVerifiedRegularFile(%q) error = nil", storagePath)
			}
		})
	}
	empty := verifiedAuthorizedMount(t, root, ".")
	leaf, _, err := OpenVerifiedRegularFile(empty)
	if leaf != nil {
		leaf.Close()
	}
	if !errors.Is(err, ErrNotFile) {
		t.Fatalf("OpenVerifiedRegularFile(.) error = %v, want ErrNotFile", err)
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
