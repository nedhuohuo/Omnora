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
