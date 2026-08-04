package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/domain"
)

func TestSoftDeleteListAndRestoreTrash(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "hello")
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}

	item, err := NewService().SoftDelete(mount, "notes.txt")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected source removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(AbsTrashDir(root), item.ID, "notes.txt")); err != nil {
		t.Fatalf("expected trash object: %v", err)
	}

	items, err := NewService().ListTrash(mount)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("ListTrash = %#v, want one item %s", items, item.ID)
	}

	restored, err := NewService().RestoreTrash(mount, item.ID)
	if err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}
	if restored != "notes.txt" {
		t.Fatalf("restored path = %q, want notes.txt", restored)
	}
	body, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(body) != "hello" {
		t.Fatalf("restored body = %q, want hello", string(body))
	}
}

func TestSoftDeleteRequiresManagedMount(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "hello")
	_, err := NewService().SoftDelete(Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "external"}, "notes.txt")
	if !errors.Is(err, ErrNotManagedMount) {
		t.Fatalf("SoftDelete error = %v, want ErrNotManagedMount", err)
	}
}

func TestPurgeAndEmptyTrash(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}

	itemA, err := NewService().SoftDelete(mount, "a.txt")
	if err != nil {
		t.Fatalf("SoftDelete a.txt: %v", err)
	}
	itemB, err := NewService().SoftDelete(mount, "b.txt")
	if err != nil {
		t.Fatalf("SoftDelete b.txt: %v", err)
	}

	if err := NewService().PurgeTrash(mount, itemA.ID); err != nil {
		t.Fatalf("PurgeTrash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(AbsTrashDir(root), itemA.ID)); !os.IsNotExist(err) {
		t.Fatalf("purged item A should be gone, stat err = %v", err)
	}
	items, err := NewService().ListTrash(mount)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(items) != 1 || items[0].ID != itemB.ID {
		t.Fatalf("ListTrash after purge = %#v, want only item B", items)
	}

	removed, err := NewService().EmptyTrash(mount)
	if err != nil {
		t.Fatalf("EmptyTrash: %v", err)
	}
	if removed != 1 {
		t.Fatalf("EmptyTrash removed = %d, want 1", removed)
	}
	items, err = NewService().ListTrash(mount)
	if err != nil {
		t.Fatalf("ListTrash after empty: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ListTrash after empty = %#v, want none", items)
	}
}

func TestPurgeTrashRejectsUnknownID(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}
	if _, err := NewService().SoftDelete(mount, "a.txt"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := NewService().PurgeTrash(mount, "trash_missing"); !errors.Is(err, ErrTrashItemNotFound) {
		t.Fatalf("PurgeTrash(missing) error = %v, want ErrTrashItemNotFound", err)
	}
}

func TestTrashRejectsTraversalIDs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "doc.txt"), "data")
	mount := Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}
	item, err := NewService().SoftDelete(mount, "doc.txt")
	if err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	for _, bad := range []string{"..", ".", "/x", "a/b", ".hidden"} {
		if _, err := NewService().RestoreTrash(mount, bad); !errors.Is(err, ErrTrashItemNotFound) {
			t.Fatalf("RestoreTrash(%q) error = %v, want ErrTrashItemNotFound", bad, err)
		}
		if err := NewService().PurgeTrash(mount, bad); !errors.Is(err, ErrTrashItemNotFound) {
			t.Fatalf("PurgeTrash(%q) error = %v, want ErrTrashItemNotFound", bad, err)
		}
	}
	items, err := NewService().ListTrash(mount)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("ListTrash after traversal attempts = %#v, want one item %s", items, item.ID)
	}
}

func TestCopyAndMoveAcrossMounts(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	writeFile(t, filepath.Join(srcRoot, "report.pdf"), "pdf-bytes")
	source := Mount{Root: srcRoot, Mode: domain.MountModeReadWrite, Kind: "external"}
	dest := Mount{Root: dstRoot, Mode: domain.MountModeReadWrite, Kind: "managed"}

	copied, err := NewService().CopyAcrossMounts(source, dest, "report.pdf", ".")
	if err != nil {
		t.Fatalf("CopyAcrossMounts: %v", err)
	}
	if copied != "report.pdf" {
		t.Fatalf("copied = %q, want report.pdf", copied)
	}
	if _, err := os.Stat(filepath.Join(srcRoot, "report.pdf")); err != nil {
		t.Fatalf("source should remain after copy: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dstRoot, "report.pdf"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(body) != "pdf-bytes" {
		t.Fatalf("copied body = %q", string(body))
	}

	writeFile(t, filepath.Join(srcRoot, "move-me.txt"), "move")
	moved, err := NewService().MoveAcrossMounts(source, dest, "move-me.txt", ".")
	if err != nil {
		t.Fatalf("MoveAcrossMounts: %v", err)
	}
	if moved != "move-me.txt" {
		t.Fatalf("moved = %q, want move-me.txt", moved)
	}
	if _, err := os.Stat(filepath.Join(srcRoot, "move-me.txt")); !os.IsNotExist(err) {
		t.Fatalf("source should be deleted after move, stat err = %v", err)
	}
	body, err = os.ReadFile(filepath.Join(dstRoot, "move-me.txt"))
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(body) != "move" {
		t.Fatalf("moved body = %q", string(body))
	}
}
