package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/storage"
)

const trashDirName = "trash"

var (
	ErrCrossMountIncomplete = errors.New("cross-mount operation incomplete")
	ErrNotManagedMount      = errors.New("operation requires a managed mount")
	ErrTrashItemNotFound    = errors.New("trash item was not found")
)

type TrashItem struct {
	ID                string    `json:"id"`
	OriginalPath      string    `json:"originalPath"`
	Name              string    `json:"name"`
	Kind              EntryKind `json:"kind"`
	Size              int64     `json:"size"`
	DeletedAt         time.Time `json:"deletedAt"`
	TrashRelativePath string    `json:"trashRelativePath"`
}

type trashMeta struct {
	ID           string `json:"id"`
	OriginalPath string `json:"originalPath"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Size         int64  `json:"size"`
	DeletedAt    string `json:"deletedAt"`
}

// SoftDelete moves a managed-mount object into `.omnora/trash/<id>/`.
func (Service) SoftDelete(mount Mount, relativePath string) (TrashItem, error) {
	return (Service{}).SoftDeleteWithID(mount, relativePath, "trash_"+httpx.NewRequestID())
}

// SoftDeleteWithID behaves like SoftDelete but accepts a caller-supplied
// trash ID. Callers that durably journal the operation before performing
// filesystem I/O (see internal/fileops) generate the ID up front so it can
// be recorded in the operation row before the move happens.
func (Service) SoftDeleteWithID(mount Mount, relativePath, id string) (TrashItem, error) {
	if err := requireManagedWritable(mount); err != nil {
		return TrashItem{}, err
	}
	if !validTrashID(id) {
		return TrashItem{}, ErrTrashItemNotFound
	}
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil || cleaned == "." {
		return TrashItem{}, ErrNotFile
	}
	root, _, err := openMountRoot(mount)
	if err != nil {
		return TrashItem{}, err
	}
	defer root.Close()
	if err := rejectSymlinkPath(root, cleaned); err != nil {
		return TrashItem{}, err
	}
	info, err := root.Lstat(cleaned)
	if err != nil {
		return TrashItem{}, err
	}
	kind, ok := entryKind(info)
	if !ok {
		return TrashItem{}, ErrNotFile
	}
	trashRoot := path.Join(storage.ReservedNamespace, trashDirName, id)
	if err := ensureReservedDirs(root); err != nil {
		return TrashItem{}, err
	}
	if err := root.Mkdir(trashRoot, 0o755); err != nil {
		return TrashItem{}, err
	}
	destName := path.Base(cleaned)
	destRel := path.Join(trashRoot, destName)
	if err := root.Rename(cleaned, destRel); err != nil {
		_ = root.RemoveAll(trashRoot)
		return TrashItem{}, err
	}
	size := info.Size()
	if kind == EntryKindDir {
		size = 0
	}
	meta := trashMeta{
		ID:           id,
		OriginalPath: cleaned,
		Name:         destName,
		Kind:         string(kind),
		Size:         size,
		DeletedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeTrashMeta(root, trashRoot, meta); err != nil {
		// Best-effort rollback: move object back.
		_ = root.Rename(destRel, cleaned)
		_ = root.RemoveAll(trashRoot)
		return TrashItem{}, err
	}
	deletedAt, _ := time.Parse(time.RFC3339Nano, meta.DeletedAt)
	return TrashItem{
		ID:                id,
		OriginalPath:      cleaned,
		Name:              destName,
		Kind:              kind,
		Size:              size,
		DeletedAt:         deletedAt,
		TrashRelativePath: destRel,
	}, nil
}

// ListTrash returns soft-deleted items for a managed mount.
func (Service) ListTrash(mount Mount) ([]TrashItem, error) {
	if mount.Kind != "managed" {
		return nil, ErrNotManagedMount
	}
	root, _, err := openMountRoot(filesMount(mount))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	trashParent := path.Join(storage.ReservedNamespace, trashDirName)
	directory, err := root.Open(trashParent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TrashItem{}, nil
		}
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	items := make([]TrashItem, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item, err := readTrashItem(root, path.Join(trashParent, entry.Name()))
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// RestoreTrash moves a trash item back to its original path when available,
// otherwise restores beside the original parent with a conflict-safe name.
func (Service) RestoreTrash(mount Mount, trashID string) (string, error) {
	if err := requireManagedWritable(mount); err != nil {
		return "", err
	}
	trashID = strings.TrimSpace(trashID)
	if !validTrashID(trashID) {
		return "", ErrTrashItemNotFound
	}
	root, _, err := openMountRoot(mount)
	if err != nil {
		return "", err
	}
	defer root.Close()
	trashRoot := path.Join(storage.ReservedNamespace, trashDirName, trashID)
	item, err := readTrashItem(root, trashRoot)
	if err != nil {
		return "", err
	}
	target := item.OriginalPath
	if _, err := root.Lstat(target); err == nil {
		target = conflictSafePath(item.OriginalPath, trashID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := path.Dir(target)
	if parent != "." {
		if err := mkdirAllRoot(root, parent); err != nil {
			return "", err
		}
	}
	if err := renameNoReplace(root, item.TrashRelativePath, target, item.Kind); err != nil {
		return "", err
	}
	_ = root.RemoveAll(trashRoot)
	return target, nil
}

// PurgeTrash permanently removes a single soft-deleted item.
func (Service) PurgeTrash(mount Mount, trashID string) error {
	if err := requireManagedWritable(mount); err != nil {
		return err
	}
	trashID = strings.TrimSpace(trashID)
	if !validTrashID(trashID) {
		return ErrTrashItemNotFound
	}
	root, _, err := openMountRoot(mount)
	if err != nil {
		return err
	}
	defer root.Close()
	trashRoot := path.Join(storage.ReservedNamespace, trashDirName, trashID)
	info, err := root.Lstat(trashRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrTrashItemNotFound
		}
		return err
	}
	if !info.IsDir() {
		return ErrTrashItemNotFound
	}
	return root.RemoveAll(trashRoot)
}

// EmptyTrash permanently removes every soft-deleted item on a managed mount.
func (Service) EmptyTrash(mount Mount) (int, error) {
	if err := requireManagedWritable(mount); err != nil {
		return 0, err
	}
	root, _, err := openMountRoot(mount)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	trashParent := path.Join(storage.ReservedNamespace, trashDirName)
	directory, err := root.Open(trashParent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := root.RemoveAll(path.Join(trashParent, entry.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// validTrashID rejects IDs that could escape the trash namespace (such as ".."
// or ".") and anything carrying a path separator.
func validTrashID(id string) bool {
	if id == "" || strings.HasPrefix(id, ".") {
		return false
	}
	return !strings.ContainsAny(id, "/\\")
}

// CopyAcrossMounts streams a file or directory tree from source to dest.
func (Service) CopyAcrossMounts(source, dest Mount, from, toDir string) (string, error) {
	cleanedFrom, err := storage.CleanRelativePath(from)
	if err != nil || cleanedFrom == "." {
		return "", ErrNotFile
	}
	destDir, err := cleanDirectory(toDir)
	if err != nil {
		return "", err
	}
	destPath := joinRelativePath(destDir, path.Base(cleanedFrom))
	srcRoot, _, err := openMountRoot(source)
	if err != nil {
		return "", err
	}
	defer srcRoot.Close()
	dstRoot, dstReadOnly, err := openMountRoot(dest)
	if err != nil {
		return "", err
	}
	defer dstRoot.Close()
	if dstReadOnly {
		return "", ErrInvalidMountMode
	}
	if err := rejectSymlinkPath(srcRoot, cleanedFrom); err != nil {
		return "", err
	}
	if err := rejectSymlinkPath(dstRoot, destDir); err != nil {
		return "", err
	}
	if _, err := dstRoot.Lstat(destPath); err == nil {
		return "", fmt.Errorf("%w: target already exists", ErrInvalidMount)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	info, err := srcRoot.Lstat(cleanedFrom)
	if err != nil {
		return "", err
	}
	kind, ok := entryKind(info)
	if !ok {
		return "", ErrNotFile
	}
	// Build under a unique sibling staging name. The final publication uses a
	// no-replace operation, so a target created after the initial Lstat is
	// preserved and only our staging object is cleaned up on failure.
	stagePath := path.Join(destDir, ".omnora-copy-"+httpx.NewRequestID())
	if kind == EntryKindDir {
		if err := copyDirTree(srcRoot, dstRoot, cleanedFrom, stagePath); err != nil {
			if !errors.Is(err, os.ErrExist) {
				_ = dstRoot.RemoveAll(stagePath)
			}
			return "", err
		}
	} else if err := copyFileVerified(srcRoot, dstRoot, cleanedFrom, stagePath, info.Size()); err != nil {
		if !errors.Is(err, os.ErrExist) {
			_ = dstRoot.Remove(stagePath)
		}
		return "", err
	}
	if err := renameNoReplace(dstRoot, stagePath, destPath, kind); err != nil {
		_ = dstRoot.RemoveAll(stagePath)
		return "", err
	}
	return destPath, nil
}

// MoveAcrossMounts copies then deletes the source after verifying the destination.
func (Service) MoveAcrossMounts(source, dest Mount, from, toDir string) (string, error) {
	srcRoot, srcReadOnly, err := openMountRoot(source)
	if err != nil {
		return "", err
	}
	srcRoot.Close()
	if srcReadOnly {
		return "", ErrInvalidMountMode
	}
	copied, err := (Service{}).CopyAcrossMounts(source, dest, from, toDir)
	if err != nil {
		return "", err
	}
	if err := (Service{}).Delete(source, from); err != nil {
		return copied, fmt.Errorf("%w: copied to %s but source delete failed: %v", ErrCrossMountIncomplete, copied, err)
	}
	return copied, nil
}

func requireManagedWritable(mount Mount) error {
	if mount.Kind != "managed" {
		return ErrNotManagedMount
	}
	if mount.Mode != domain.MountModeReadWrite {
		return ErrInvalidMountMode
	}
	return nil
}

func filesMount(mount Mount) Mount { return mount }

func ensureReservedDirs(root *os.Root) error {
	if err := root.Mkdir(storage.ReservedNamespace, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	trashParent := path.Join(storage.ReservedNamespace, trashDirName)
	if err := root.Mkdir(trashParent, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func writeTrashMeta(root *os.Root, trashRoot string, meta trashMeta) error {
	payload, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(path.Join(trashRoot, "meta.json"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	return file.Sync()
}

func readTrashItem(root *os.Root, trashRoot string) (TrashItem, error) {
	file, err := root.Open(path.Join(trashRoot, "meta.json"))
	if err != nil {
		return TrashItem{}, ErrTrashItemNotFound
	}
	defer file.Close()
	var meta trashMeta
	if err := json.NewDecoder(file).Decode(&meta); err != nil {
		return TrashItem{}, err
	}
	deletedAt, _ := time.Parse(time.RFC3339Nano, meta.DeletedAt)
	objectPath := path.Join(trashRoot, meta.Name)
	return TrashItem{
		ID:                meta.ID,
		OriginalPath:      meta.OriginalPath,
		Name:              meta.Name,
		Kind:              EntryKind(meta.Kind),
		Size:              meta.Size,
		DeletedAt:         deletedAt,
		TrashRelativePath: objectPath,
	}, nil
}

func conflictSafePath(original, trashID string) string {
	ext := path.Ext(original)
	base := strings.TrimSuffix(original, ext)
	return fmt.Sprintf("%s.restored-%s%s", base, trashID, ext)
}

func cleanDirectory(value string) (string, error) {
	cleaned, err := storage.CleanRelativePath(value)
	if err != nil {
		return "", err
	}
	return cleaned, nil
}

// renameNoReplace uses link-then-unlink for regular files, which is atomic
// and cannot overwrite a racing destination. Directories cannot be hard
// linked; they are renamed only after the destination absence check and are
// assigned a collision-safe path by RestoreTrash.
func renameNoReplace(root *os.Root, source, destination string, kind EntryKind) error {
	if kind == EntryKindFile {
		if err := root.Link(source, destination); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: target already exists", ErrInvalidMount)
			}
			return err
		}
		if err := root.Remove(source); err != nil {
			_ = root.Remove(destination)
			return err
		}
		return nil
	}
	if _, err := root.Lstat(destination); err == nil {
		return fmt.Errorf("%w: target already exists", ErrInvalidMount)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.Rename(source, destination)
}

func mkdirAllRoot(root *os.Root, relative string) error {
	relative = path.Clean(relative)
	if relative == "." {
		return nil
	}
	parts := strings.Split(relative, "/")
	current := ""
	for _, part := range parts {
		if current == "" {
			current = part
		} else {
			current = path.Join(current, part)
		}
		if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func copyDirTree(src, dst *os.Root, from, to string) error {
	if err := dst.Mkdir(to, 0o755); err != nil {
		return err
	}
	directory, err := src.Open(from)
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		childFrom := path.Join(from, name)
		childTo := path.Join(to, name)
		info, err := src.Lstat(childFrom)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			if err := copyDirTree(src, dst, childFrom, childTo); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := copyFileVerified(src, dst, childFrom, childTo, info.Size()); err != nil {
			return err
		}
	}
	return nil
}

func copyFileVerified(src, dst *os.Root, from, to string, expectedSize int64) error {
	in, err := src.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := dst.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 32*1024)
	written, err := io.CopyBuffer(out, in, buf)
	if err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if written != expectedSize {
		return fmt.Errorf("copied size mismatch: wrote %d, expected %d", written, expectedSize)
	}
	info, err := dst.Lstat(to)
	if err != nil {
		return err
	}
	if info.Size() != expectedSize {
		return fmt.Errorf("destination size mismatch: got %d, expected %d", info.Size(), expectedSize)
	}
	return nil
}

// AbsTrashDir is exposed for tests.
func AbsTrashDir(mountRoot string) string {
	return filepath.Join(mountRoot, storage.ReservedNamespace, trashDirName)
}
