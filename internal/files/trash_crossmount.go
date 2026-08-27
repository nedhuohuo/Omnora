package files

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/storage"
)

const (
	trashDirName         = "trash"
	MaxPersonalTrashItem = int64(200 * 1024 * 1024)
)

var (
	ErrCrossMountIncomplete = errors.New("cross-mount operation incomplete")
	ErrNotManagedMount      = errors.New("operation requires a managed mount")
	ErrTrashItemNotFound    = errors.New("trash item was not found")
	ErrTrashTooLarge        = errors.New("object exceeds the 200 MiB personal trash limit; confirmed permanent delete is required")
)

type TrashItem struct {
	ID                string    `json:"id"`
	OriginalPath      string    `json:"originalPath"`
	Name              string    `json:"name"`
	Kind              EntryKind `json:"kind"`
	Size              int64     `json:"size"`
	DeletedAt         time.Time `json:"deletedAt"`
	TrashRelativePath string    `json:"trashRelativePath"`
	RecoveredOnly     bool      `json:"-"`
}

type trashMeta struct {
	ID            string `json:"id"`
	OriginalPath  string `json:"originalPath"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Size          int64  `json:"size"`
	DeletedAt     string `json:"deletedAt"`
	RecoveredOnly bool   `json:"recoveredOnly,omitempty"`
}

// SoftDeleteToPersonalTrash copies a writable source object into the deleting
// account's managed trash, verifies the copy, and only then removes source.
func (Service) SoftDeleteToPersonalTrash(source, personal Mount, relativePath string) (TrashItem, error) {
	return (Service{}).SoftDeleteToPersonalTrashWithID(source, personal, relativePath, "trash_"+httpx.NewRequestID())
}

func (Service) SoftDeleteToPersonalTrashWithID(source, personal Mount, relativePath, id string) (TrashItem, error) {
	if source.Mode != domain.MountModeReadWrite {
		return TrashItem{}, ErrInvalidMountMode
	}
	if err := requireManagedWritable(personal); err != nil {
		return TrashItem{}, err
	}
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil || cleaned == "." {
		return TrashItem{}, ErrNotFile
	}
	if !validTrashID(id) {
		return TrashItem{}, ErrTrashItemNotFound
	}
	srcRoot, _, err := openMountRoot(source)
	if err != nil {
		return TrashItem{}, err
	}
	defer srcRoot.Close()
	dstRoot, _, err := openMountRoot(personal)
	if err != nil {
		return TrashItem{}, err
	}
	defer dstRoot.Close()
	if err := rejectSymlinkPath(srcRoot, cleaned); err != nil {
		return TrashItem{}, err
	}
	info, err := srcRoot.Lstat(cleaned)
	if err != nil {
		return TrashItem{}, err
	}
	kind, ok := entryKind(info)
	if !ok {
		return TrashItem{}, ErrNotFile
	}
	size, err := trashObjectSize(srcRoot, cleaned, MaxPersonalTrashItem)
	if err != nil {
		return TrashItem{}, err
	}
	trashRoot := path.Join(storage.ReservedNamespace, trashDirName, id)
	if err := ensureReservedDirs(dstRoot); err != nil {
		return TrashItem{}, err
	}
	if err := dstRoot.Mkdir(trashRoot, 0o755); err != nil {
		return TrashItem{}, err
	}
	name := path.Base(cleaned)
	destination := path.Join(trashRoot, name)
	if kind == EntryKindDir {
		err = copyDirTree(srcRoot, dstRoot, cleaned, destination)
	} else {
		err = copyFileVerified(srcRoot, dstRoot, cleaned, destination, info.Size())
	}
	if err != nil {
		_ = dstRoot.RemoveAll(trashRoot)
		return TrashItem{}, err
	}
	sourceManifest, err := captureTreeNamed(srcRoot, cleaned, name)
	if err != nil {
		_ = dstRoot.RemoveAll(trashRoot)
		return TrashItem{}, err
	}
	destinationManifest, err := captureTreeNamed(dstRoot, destination, name)
	if err != nil || destinationManifest != sourceManifest {
		_ = dstRoot.RemoveAll(trashRoot)
		return TrashItem{}, errors.Join(ErrCrossMountIncomplete, err)
	}
	deletedAt := time.Now().UTC()
	meta := trashMeta{ID: id, OriginalPath: cleaned, Name: name, Kind: string(kind), Size: size, DeletedAt: deletedAt.Format(time.RFC3339Nano), RecoveredOnly: true}
	if err := writeTrashMeta(dstRoot, trashRoot, meta); err != nil {
		_ = dstRoot.RemoveAll(trashRoot)
		return TrashItem{}, err
	}
	if kind == EntryKindDir {
		err = srcRoot.RemoveAll(cleaned)
	} else {
		err = srcRoot.Remove(cleaned)
	}
	if err != nil {
		_ = dstRoot.RemoveAll(trashRoot)
		return TrashItem{}, fmt.Errorf("%w: source cleanup failed: %v", ErrCrossMountIncomplete, err)
	}
	return TrashItem{ID: id, OriginalPath: cleaned, Name: name, Kind: kind, Size: size, DeletedAt: deletedAt, TrashRelativePath: destination, RecoveredOnly: true}, nil
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
	size, err := trashObjectSize(root, cleaned, MaxPersonalTrashItem)
	if err != nil {
		return TrashItem{}, err
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

func trashObjectSize(root *os.Root, relative string, limit int64) (int64, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, ErrNotFile
	}
	if info.Mode().IsRegular() {
		if info.Size() > limit {
			return 0, ErrTrashTooLarge
		}
		return info.Size(), nil
	}
	if !info.IsDir() {
		return 0, ErrNotFile
	}
	directory, err := root.Open(relative)
	if err != nil {
		return 0, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	var total int64
	for _, entry := range entries {
		remaining := limit - total
		if remaining < 0 {
			return 0, ErrTrashTooLarge
		}
		size, err := trashObjectSize(root, path.Join(relative, entry.Name()), remaining)
		if err != nil {
			return 0, err
		}
		total += size
		if total > limit {
			return 0, ErrTrashTooLarge
		}
	}
	return total, nil
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
	parent := path.Dir(target)
	if item.RecoveredOnly || !restorableOriginalParent(root, parent) {
		target = path.Join("Recovered Files", trashID, item.Name)
		parent = path.Dir(target)
	} else if _, err := root.Lstat(target); err == nil {
		target = path.Join("Recovered Files", trashID, item.Name)
		parent = path.Dir(target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
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

func restorableOriginalParent(root *os.Root, parent string) bool {
	if parent == "." {
		return true
	}
	if err := rejectSymlinkPath(root, parent); err != nil {
		return false
	}
	info, err := root.Lstat(parent)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
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

// MoveAcrossMounts uses an operation-scoped source staging area so a source
// path cannot be changed or partially deleted while the destination is being
// built. The legacy entry point still works, but callers that have a durable
// operation ID should use MoveAcrossMountsWithOperationID.
func (Service) MoveAcrossMounts(source, dest Mount, from, toDir string) (string, error) {
	return (Service{}).MoveAcrossMountsWithOperationID(source, dest, from, toDir, "cross-mount-"+httpx.NewRequestID())
}

// MoveAcrossMountsWithOperationID stages the source under the reserved
// operation namespace, verifies the complete source and destination trees,
// publishes the destination without replacement, and removes source staging
// only after publication. If cleanup is ambiguous, both the published target
// and operation-scoped source remain and ErrCrossMountIncomplete is returned.
func (Service) MoveAcrossMountsWithOperationID(source, dest Mount, from, toDir, operationID string) (string, error) {
	cleanedFrom, err := storage.CleanRelativePath(from)
	if err != nil || cleanedFrom == "." {
		return "", ErrNotFile
	}
	if operationID == "" || strings.ContainsAny(operationID, "/\\") {
		return "", ErrCrossMountIncomplete
	}
	destDir, err := cleanDirectory(toDir)
	if err != nil {
		return "", err
	}
	destPath := joinRelativePath(destDir, path.Base(cleanedFrom))
	srcRoot, srcReadOnly, err := openMountRoot(source)
	if err != nil {
		return "", err
	}
	defer srcRoot.Close()
	if srcReadOnly {
		return "", ErrInvalidMountMode
	}
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

	stageRoot := path.Join(storage.ReservedNamespace, "operations", operationID)
	stagePath := path.Join(stageRoot, "source")
	if err := mkdirAllRoot(srcRoot, stageRoot); err != nil {
		return "", err
	}
	if err := srcRoot.Rename(cleanedFrom, stagePath); err != nil {
		_ = srcRoot.RemoveAll(stageRoot)
		return "", err
	}
	restoreSource := func() error {
		if _, statErr := srcRoot.Lstat(cleanedFrom); statErr == nil {
			return fmt.Errorf("source path is occupied")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		return renameNoReplace(srcRoot, stagePath, cleanedFrom, kind)
	}
	cleanupDestination := func(stageDestination string) error {
		if stageDestination == "" {
			return nil
		}
		if err := dstRoot.RemoveAll(stageDestination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	failBeforePublish := func(cause error, stageDestination string) (string, error) {
		cleanupErr := cleanupDestination(stageDestination)
		restoreErr := restoreSource()
		if cleanupErr != nil || restoreErr != nil {
			return "", fmt.Errorf("%w: operation %s retained source staging: %v", ErrCrossMountIncomplete, operationID, errors.Join(cause, cleanupErr, restoreErr))
		}
		return "", cause
	}

	logicalName := path.Base(cleanedFrom)
	sourceManifest, err := captureTreeNamed(srcRoot, stagePath, logicalName)
	if err != nil {
		return failBeforePublish(err, "")
	}
	stageDestination := path.Join(destDir, ".omnora-copy-"+operationID)
	if kind == EntryKindDir {
		err = copyDirTree(srcRoot, dstRoot, stagePath, stageDestination)
	} else {
		err = copyFileVerified(srcRoot, dstRoot, stagePath, stageDestination, info.Size())
	}
	if err != nil {
		return failBeforePublish(err, stageDestination)
	}
	if destinationManifest, manifestErr := captureTreeNamed(dstRoot, stageDestination, logicalName); manifestErr != nil {
		return failBeforePublish(manifestErr, stageDestination)
	} else if destinationManifest != sourceManifest {
		return failBeforePublish(fmt.Errorf("destination manifest differs from source"), stageDestination)
	}
	if err := renameNoReplace(dstRoot, stageDestination, destPath, kind); err != nil {
		return failBeforePublish(err, stageDestination)
	}
	if sourceAfter, manifestErr := captureTreeNamed(srcRoot, stagePath, logicalName); manifestErr != nil {
		return destPath, fmt.Errorf("%w: operation %s source verification failed after publication: %v", ErrCrossMountIncomplete, operationID, manifestErr)
	} else if sourceAfter != sourceManifest {
		return destPath, fmt.Errorf("%w: operation %s source changed during copy", ErrCrossMountIncomplete, operationID)
	}
	if err := srcRoot.RemoveAll(stageRoot); err != nil {
		return destPath, fmt.Errorf("%w: operation %s source cleanup failed after publishing %s: %v", ErrCrossMountIncomplete, operationID, destPath, err)
	}
	return destPath, nil
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
		RecoveredOnly:     meta.RecoveredOnly,
	}, nil
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
			return ErrNotFile
		}
		if info.IsDir() {
			if err := copyDirTree(src, dst, childFrom, childTo); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return ErrNotFile
		}
		if err := copyFileVerified(src, dst, childFrom, childTo, info.Size()); err != nil {
			return err
		}
	}
	return nil
}

// captureTree returns a deterministic digest of a regular-file/directory
// tree. Unsupported entries are rejected instead of being silently skipped;
// otherwise a move could delete source data that was never copied.
func captureTree(root *os.Root, relative string) (string, error) {
	return captureTreeNamed(root, relative, path.Base(relative))
}

func captureTreeNamed(root *os.Root, relative, displayName string) (string, error) {
	digest := sha256.New()
	var walk func(string, string) error
	walk = func(current, display string) error {
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrNotFile
		}
		kind := "file"
		if info.IsDir() {
			kind = "dir"
		}
		size := info.Size()
		if info.IsDir() {
			// Directory st_size is filesystem-specific; it is not part of the
			// logical tree and would make an otherwise identical cross-mount
			// copy fail verification on different filesystems.
			size = 0
		}
		fmt.Fprintf(digest, "path=%s|kind=%s|size=%d\n", display, kind, size)
		if info.IsDir() {
			directory, err := root.Open(current)
			if err != nil {
				return err
			}
			entries, err := directory.ReadDir(-1)
			closeErr := directory.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				name := entry.Name()
				child := path.Join(current, name)
				childDisplay := path.Join(display, name)
				if err := walk(child, childDisplay); err != nil {
					return err
				}
			}
			return nil
		}
		file, err := root.Open(current)
		if err != nil {
			return err
		}
		content := sha256.New()
		_, copyErr := io.Copy(content, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(digest, "sha256=%x\n", content.Sum(nil))
		return nil
	}
	if err := walk(relative, displayName); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
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
