package files

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/storage"
)

type EntryKind string

const (
	EntryKindDir  EntryKind = "dir"
	EntryKindFile EntryKind = "file"
)

type PreviewKind string

const (
	PreviewKindImage           PreviewKind = "image"
	PreviewKindPDF             PreviewKind = "pdf"
	PreviewKindText            PreviewKind = "text"
	PreviewKindMarkdown        PreviewKind = "markdown"
	PreviewKindMedia           PreviewKind = "media"
	PreviewKindOfficeDownload  PreviewKind = "office_download"
	PreviewKindUnknownDownload PreviewKind = "unknown_download"
)

var (
	ErrInvalidMount     = errors.New("invalid mount")
	ErrInvalidMountMode = errors.New("invalid mount mode")
	ErrNotDirectory     = errors.New("not a directory")
	ErrNotFile          = errors.New("not a regular file")
	ErrNotShareable     = errors.New("not a shareable file or directory")
	ErrSymlinkPath      = errors.New("symbolic links are not allowed")
)

type Mount struct {
	Root string
	Mode domain.MountMode
	Kind string
}

type DirectoryListing struct {
	RelativePath string  `json:"relativePath"`
	ReadOnly     bool    `json:"readOnly"`
	Entries      []Entry `json:"entries"`
}

type Entry struct {
	Name              string      `json:"name"`
	RelativePath      string      `json:"relativePath"`
	Kind              EntryKind   `json:"kind"`
	Size              int64       `json:"size"`
	ModifiedAt        time.Time   `json:"modifiedAt"`
	ReadOnly          bool        `json:"readOnly"`
	PreviewKind       PreviewKind `json:"previewKind"`
	ObjectFingerprint string      `json:"objectFingerprint,omitempty"`
}

type Service struct{}

func NewService() Service {
	return Service{}
}

func (Service) ListDirectory(mount Mount, relativePath string) (DirectoryListing, error) {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil {
		return DirectoryListing{}, err
	}

	root, readOnly, err := openMountRoot(mount)
	if err != nil {
		return DirectoryListing{}, err
	}
	defer root.Close()
	if err := rejectSymlinkPath(root, cleaned); err != nil {
		return DirectoryListing{}, err
	}
	directory, err := root.Open(cleaned)
	if err != nil {
		return DirectoryListing{}, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return DirectoryListing{}, err
	}
	if !info.IsDir() {
		return DirectoryListing{}, fmt.Errorf("%w: %s", ErrNotDirectory, cleaned)
	}

	dirEntries, err := directory.ReadDir(-1)
	if err != nil {
		return DirectoryListing{}, err
	}

	entries := make([]Entry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		name := dirEntry.Name()
		entryRelativePath := joinRelativePath(cleaned, name)
		if _, err := storage.CleanRelativePath(entryRelativePath); err != nil {
			continue
		}

		entryInfo, err := root.Lstat(entryRelativePath)
		if err != nil {
			return DirectoryListing{}, err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}

		kind, ok := entryKind(entryInfo)
		if !ok {
			continue
		}

		size := entryInfo.Size()
		if kind == EntryKindDir {
			size = 0
		}

		entries = append(entries, Entry{
			Name:              name,
			RelativePath:      entryRelativePath,
			Kind:              kind,
			Size:              size,
			ModifiedAt:        entryInfo.ModTime().UTC(),
			ReadOnly:          readOnly,
			PreviewKind:       classifyPreviewKind(name, kind),
			ObjectFingerprint: fingerprintFileInfo(entryInfo),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == EntryKindDir
		}
		return entries[i].Name < entries[j].Name
	})

	return DirectoryListing{
		RelativePath: cleaned,
		ReadOnly:     readOnly,
		Entries:      entries,
	}, nil
}

// Stat returns metadata for one mount-relative object. It intentionally uses
// os.Root/Lstat so callers can never turn a member locator into a host path
// lookup. Symbolic links and objects other than regular files/directories are
// rejected in the same way as directory listings.
func (Service) Stat(mount Mount, relativePath string) (Entry, error) {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil || strings.Contains(cleaned, `\`) {
		if err != nil {
			return Entry{}, err
		}
		return Entry{}, fmt.Errorf("invalid relative path")
	}
	root, readOnly, err := openMountRoot(mount)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	if err := rejectSymlinkPath(root, cleaned); err != nil {
		return Entry{}, err
	}
	info, err := root.Lstat(cleaned)
	if err != nil {
		return Entry{}, err
	}
	kind, ok := entryKind(info)
	if !ok {
		return Entry{}, ErrNotFile
	}
	name := path.Base(cleaned)
	if cleaned == "." {
		name = "."
	}
	size := info.Size()
	if kind == EntryKindDir {
		size = 0
	}
	return Entry{
		Name:              name,
		RelativePath:      cleaned,
		Kind:              kind,
		Size:              size,
		ModifiedAt:        info.ModTime().UTC(),
		ReadOnly:          readOnly,
		PreviewKind:       classifyPreviewKind(name, kind),
		ObjectFingerprint: fingerprintFileInfo(info),
	}, nil
}

// fingerprintFileInfo is an opaque object identity used by higher-level
// mutation and confirmation services. It contains metadata only and never a
// host path or file contents.
func fingerprintFileInfo(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%s|%d|%d|%d", info.Mode().String(), info.Size(), info.ModTime().UTC().UnixNano(), info.Mode().Perm())
	if raw := info.Sys(); raw != nil {
		value := reflect.Indirect(reflect.ValueOf(raw))
		if !value.IsValid() || value.Kind() != reflect.Struct {
			return fmt.Sprintf("sha256:%x", digest.Sum(nil))
		}
		for _, name := range []string{"Dev", "Ino"} {
			field := value.FieldByName(name)
			if field.IsValid() && field.CanUint() {
				fmt.Fprintf(digest, "|%s=%d", name, field.Uint())
			}
		}
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil))
}

func (Service) CreateDirectory(mount Mount, parentPath, name string) (string, error) {
	parent, err := storage.CleanRelativePath(parentPath)
	if err != nil {
		return "", err
	}
	if err := validateDirectoryName(name); err != nil {
		return "", err
	}
	root, readOnly, err := openMountRoot(mount)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if readOnly {
		return "", ErrInvalidMountMode
	}
	if err := rejectSymlinkPath(root, parent); err != nil {
		return "", err
	}
	if info, err := root.Stat(parent); err != nil || !info.IsDir() {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", ErrNotDirectory, parent)
	}
	created := joinRelativePath(parent, name)
	if err := root.Mkdir(created, 0o755); err != nil {
		return "", err
	}
	return created, nil
}

// Rename moves an existing file or directory to a new relative path within
// the same mount. It refuses to overwrite an existing target and rejects
// symlinks anywhere along either path.
func (Service) Rename(mount Mount, from, to string) (string, error) {
	return moveWithinMount(mount, from, to)
}

// Move relocates an existing file or directory into a different directory
// within the same mount, keeping its base name.
func (Service) Move(mount Mount, from, toDir string) (string, error) {
	cleanedFrom, err := storage.CleanRelativePath(from)
	if err != nil || cleanedFrom == "." {
		return "", ErrNotFile
	}
	to := joinRelativePath(mustCleanDir(toDir), path.Base(cleanedFrom))
	return moveWithinMount(mount, cleanedFrom, to)
}

func mustCleanDir(dir string) string {
	cleaned, err := storage.CleanRelativePath(dir)
	if err != nil {
		return "."
	}
	return cleaned
}

func moveWithinMount(mount Mount, from, to string) (string, error) {
	cleanedFrom, err := storage.CleanRelativePath(from)
	if err != nil || cleanedFrom == "." {
		return "", ErrNotFile
	}
	cleanedTo, err := storage.CleanRelativePath(to)
	if err != nil || cleanedTo == "." {
		return "", ErrNotFile
	}
	root, readOnly, err := openMountRoot(mount)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if readOnly {
		return "", ErrInvalidMountMode
	}
	if err := rejectSymlinkPath(root, cleanedFrom); err != nil {
		return "", err
	}
	if err := rejectSymlinkPath(root, path.Dir(cleanedTo)); err != nil {
		return "", err
	}
	sourceInfo, err := root.Lstat(cleanedFrom)
	if err != nil {
		return "", err
	}
	kind, ok := entryKind(sourceInfo)
	if !ok {
		return "", ErrNotFile
	}
	if err := renameNoReplace(root, cleanedFrom, cleanedTo, kind); err != nil {
		return "", err
	}
	return cleanedTo, nil
}

// Delete removes a file or directory (and its contents, if any) from the
// mount. It refuses to delete the mount root itself.
func (Service) Delete(mount Mount, relativePath string) error {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil || cleaned == "." {
		return ErrNotFile
	}
	root, readOnly, err := openMountRoot(mount)
	if err != nil {
		return err
	}
	defer root.Close()
	if readOnly {
		return ErrInvalidMountMode
	}
	if err := rejectSymlinkPath(root, cleaned); err != nil {
		return err
	}
	info, err := root.Lstat(cleaned)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return root.RemoveAll(cleaned)
	}
	return root.Remove(cleaned)
}

func (Service) OpenFile(mount Mount, relativePath string) (*os.File, os.FileInfo, error) {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil || cleaned == "." {
		return nil, nil, ErrNotFile
	}
	root, _, err := openMountRoot(mount)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err := rejectSymlinkPath(root, cleaned); err != nil {
		return nil, nil, err
	}
	file, err := root.Open(cleaned)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, ErrNotFile
	}
	return file, info, nil
}

func (Service) ValidateWritableTarget(mount Mount, relativePath string) error {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil || cleaned == "." {
		return ErrNotFile
	}
	root, readOnly, err := openMountRoot(mount)
	if err != nil {
		return err
	}
	defer root.Close()
	if readOnly {
		return ErrInvalidMountMode
	}
	parent := path.Dir(cleaned)
	if err := rejectSymlinkPath(root, parent); err != nil {
		return err
	}
	info, err := root.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotDirectory, parent)
	}
	if info, err := root.Lstat(cleaned); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrSymlinkPath
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (Service) ValidateShareTarget(mount Mount, relativePath string) (string, error) {
	cleaned, err := storage.CleanRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	root, _, err := openMountRoot(mount)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := rejectSymlinkPath(root, cleaned); err != nil {
		return "", err
	}
	info, err := root.Lstat(cleaned)
	if err != nil {
		return "", err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", ErrNotShareable
	}
	return cleaned, nil
}

func openMountRoot(mount Mount) (*os.Root, bool, error) {
	readOnly, err := mountReadOnly(mount.Mode)
	if err != nil {
		return nil, false, err
	}
	rootPath := strings.TrimSpace(mount.Root)
	if rootPath == "" {
		return nil, false, ErrInvalidMount
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidMount, err)
	}
	return root, readOnly, nil
}

func rejectSymlinkPath(root *os.Root, relativePath string) error {
	if relativePath == "." {
		return nil
	}
	parts := strings.Split(relativePath, "/")
	for index := range parts {
		current := strings.Join(parts[:index+1], "/")
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkPath, current)
		}
	}
	return nil
}

func validateDirectoryName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == storage.ReservedNamespace || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid directory name")
	}
	for _, r := range name {
		if r == 0 || r < 0x20 || r == 0x7f {
			return errors.New("invalid directory name")
		}
	}
	return nil
}

func mountReadOnly(mode domain.MountMode) (bool, error) {
	switch mode {
	case domain.MountModeReadOnly:
		return true, nil
	case domain.MountModeReadWrite:
		return false, nil
	default:
		return false, ErrInvalidMountMode
	}
}

func joinRelativePath(base, name string) string {
	if base == "." {
		return name
	}
	return path.Join(base, name)
}

func entryKind(info os.FileInfo) (EntryKind, bool) {
	switch {
	case info.IsDir():
		return EntryKindDir, true
	case info.Mode().IsRegular():
		return EntryKindFile, true
	default:
		return "", false
	}
}

func classifyPreviewKind(name string, kind EntryKind) PreviewKind {
	if kind != EntryKindFile {
		return PreviewKindUnknownDownload
	}

	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".tif", ".tiff":
		return PreviewKindImage
	case ".pdf":
		return PreviewKindPDF
	case ".md", ".markdown":
		return PreviewKindMarkdown
	case ".txt", ".text", ".log", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml":
		return PreviewKindText
	case ".mp3", ".m4a", ".ogg", ".wav", ".flac", ".aac", ".mp4", ".m4v", ".mov", ".webm", ".ogv":
		return PreviewKindMedia
	case ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp", ".rtf":
		return PreviewKindOfficeDownload
	default:
		return PreviewKindUnknownDownload
	}
}
