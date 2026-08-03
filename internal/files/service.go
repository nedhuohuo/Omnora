package files

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
)

type Mount struct {
	Root string
	Mode domain.MountMode
}

type DirectoryListing struct {
	RelativePath string  `json:"relativePath"`
	ReadOnly     bool    `json:"readOnly"`
	Entries      []Entry `json:"entries"`
}

type Entry struct {
	Name         string      `json:"name"`
	RelativePath string      `json:"relativePath"`
	Kind         EntryKind   `json:"kind"`
	Size         int64       `json:"size"`
	ModifiedAt   time.Time   `json:"modifiedAt"`
	ReadOnly     bool        `json:"readOnly"`
	PreviewKind  PreviewKind `json:"previewKind"`
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

	readOnly, err := mountReadOnly(mount.Mode)
	if err != nil {
		return DirectoryListing{}, err
	}

	root := strings.TrimSpace(mount.Root)
	if root == "" {
		return DirectoryListing{}, ErrInvalidMount
	}

	directoryPath := filepath.Join(root, filepath.FromSlash(cleaned))
	info, err := os.Lstat(directoryPath)
	if err != nil {
		return DirectoryListing{}, err
	}
	if !info.IsDir() {
		return DirectoryListing{}, fmt.Errorf("%w: %s", ErrNotDirectory, cleaned)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return DirectoryListing{}, fmt.Errorf("%w: %s", ErrNotDirectory, cleaned)
	}

	dirEntries, err := os.ReadDir(directoryPath)
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

		entryPath := filepath.Join(directoryPath, name)
		entryInfo, err := os.Lstat(entryPath)
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
			Name:         name,
			RelativePath: entryRelativePath,
			Kind:         kind,
			Size:         size,
			ModifiedAt:   entryInfo.ModTime().UTC(),
			ReadOnly:     readOnly,
			PreviewKind:  classifyPreviewKind(name, kind),
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
