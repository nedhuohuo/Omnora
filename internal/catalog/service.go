package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"omnora/internal/contentref"
	"omnora/internal/storage"
)

const (
	DefaultBatchSize = 500
	MinBatchSize     = 200
	MaxBatchSize     = 1000

	MountStatusActive      = "active"
	MountStatusPending     = "pending"
	MountStatusDisabled    = "disabled"
	MountStatusUnavailable = "unavailable"
	MountStatusDeleted     = "deleted"
)

var (
	ErrInvalidMount  = errors.New("invalid mount")
	ErrMountExcluded = errors.New("mount excluded from indexing")
)

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func NewService(db *sql.DB) Service {
	return Service{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

type Mount struct {
	ID               string
	Source           contentref.Source
	Root             string
	Status           string
	IndexEnabled     bool
	IdentityVerified bool
	IdentityJSON     string
}

type EntryKind string

const (
	EntryKindFile      EntryKind = "file"
	EntryKindDirectory EntryKind = "directory"
)

type Entry struct {
	ID                  string
	Source              contentref.Source
	MountID             string
	RelativePath        string
	Name                string
	Kind                EntryKind
	PreviewKind         string
	SizeBytes           int64
	ModifiedAt          time.Time
	IdentityFingerprint string
}

type ScanOptions struct {
	BatchSize int
	Cursor    string
}

type ScanResult struct {
	Indexed    int
	NextCursor string
	Done       bool
}

func (s Service) ScanMount(ctx context.Context, mount Mount, opts ScanOptions) (ScanResult, error) {
	total := 0
	cursor := opts.Cursor
	for {
		opts.Cursor = cursor
		result, err := s.ScanBatch(ctx, mount, opts)
		if err != nil {
			return ScanResult{}, err
		}
		total += result.Indexed
		cursor = result.NextCursor
		if result.Done {
			result.Indexed = total
			return result, nil
		}
	}
}

func (s Service) ScanBatch(ctx context.Context, mount Mount, opts ScanOptions) (ScanResult, error) {
	if s.db == nil {
		return ScanResult{}, errors.New("catalog database is nil")
	}
	if err := validateMount(mount); err != nil {
		return ScanResult{}, err
	}

	limit := normalizeBatchSize(opts.BatchSize)
	entries, complete, err := collectBatch(ctx, mount, opts.Cursor, limit)
	if err != nil {
		return ScanResult{}, err
	}
	if len(entries) == 0 {
		return ScanResult{Done: complete}, nil
	}

	indexedAt := s.now().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScanResult{}, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO catalog_entries(
	id, mount_id, relative_path, name, entry_kind, preview_kind,
	size_bytes, modified_at, identity_fingerprint, indexed_at, deleted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(mount_id, relative_path) DO UPDATE SET
	name = excluded.name,
	entry_kind = excluded.entry_kind,
	preview_kind = excluded.preview_kind,
	size_bytes = excluded.size_bytes,
	modified_at = excluded.modified_at,
	identity_fingerprint = excluded.identity_fingerprint,
	indexed_at = excluded.indexed_at,
	deleted_at = NULL
`)
	if err != nil {
		return ScanResult{}, err
	}
	defer stmt.Close()

	for _, entry := range entries {
		if _, err := stmt.ExecContext(ctx,
			entry.ID,
			entry.MountID,
			entry.RelativePath,
			entry.Name,
			string(entry.Kind),
			entry.PreviewKind,
			entry.SizeBytes,
			entry.ModifiedAt.Format(time.RFC3339Nano),
			entry.IdentityFingerprint,
			indexedAt,
		); err != nil {
			return ScanResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ScanResult{}, err
	}

	nextCursor := entries[len(entries)-1].RelativePath
	if complete {
		nextCursor = ""
	}
	return ScanResult{
		Indexed:    len(entries),
		NextCursor: nextCursor,
		Done:       complete,
	}, nil
}

type SearchOptions struct {
	Query      string
	Limit      int
	Cursor     string
	Boundaries []SearchBoundary
}

type SearchBoundary struct {
	MountID      string
	RelativePath string
}

type SearchResult struct {
	Items          []SearchItem    `json:"items"`
	ExcludedMounts []ExcludedMount `json:"excludedMounts"`
	NextCursor     string          `json:"nextCursor"`
}

type SearchItem struct {
	ID                  string            `json:"id"`
	Source              contentref.Source `json:"source"`
	MountID             string            `json:"mountId,omitempty"`
	RelativePath        string            `json:"relativePath"`
	Name                string            `json:"name"`
	Kind                EntryKind         `json:"kind"`
	PreviewKind         string            `json:"previewKind"`
	SizeBytes           int64             `json:"sizeBytes"`
	ModifiedAt          time.Time         `json:"modifiedAt"`
	IdentityFingerprint string            `json:"identityFingerprint"`
}

type ExcludedMount struct {
	MountID string `json:"mountId"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

func (s Service) Search(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	if s.db == nil {
		return SearchResult{}, errors.New("catalog database is nil")
	}
	boundaries, err := normalizeSearchBoundaries(opts.Boundaries)
	if err != nil {
		return SearchResult{}, err
	}

	limit := normalizeSearchLimit(opts.Limit)
	cursor, err := decodeCursor(opts.Cursor)
	if err != nil {
		return SearchResult{}, err
	}

	excluded, err := s.excludedMounts(ctx, boundaries)
	if err != nil {
		return SearchResult{}, err
	}

	query := strings.ToLower(strings.TrimSpace(opts.Query))
	like := "%"
	if query != "" {
		like = "%" + escapeLike(query) + "%"
	}

	querySQL := `
	SELECT ce.id, CASE m.purpose WHEN 'personal_default' THEN 'personal' ELSE 'common_mount' END,
		ce.mount_id, ce.relative_path, ce.name, ce.entry_kind,
		ce.preview_kind, ce.size_bytes, ce.modified_at, ce.identity_fingerprint
	FROM catalog_entries ce
	JOIN mounts m ON m.id = ce.mount_id
WHERE ce.deleted_at IS NULL
	AND m.status = 'active'
		AND m.index_enabled = 1
		AND COALESCE(m.mount_identity_json, '') <> ''
		AND (? = '%' OR lower(ce.name) LIKE ? ESCAPE '\')
`
	args := []any{like, like}
	if len(boundaries) > 0 {
		clauses := make([]string, 0, len(boundaries))
		for _, boundary := range boundaries {
			if boundary.RelativePath == "" {
				clauses = append(clauses, "ce.mount_id = ?")
				args = append(args, boundary.MountID)
				continue
			}
			clauses = append(clauses, "(ce.mount_id = ? AND (ce.relative_path = ? OR ce.relative_path LIKE ? ESCAPE '\\'))")
			args = append(args, boundary.MountID, boundary.RelativePath, escapeLike(boundary.RelativePath)+"/%")
		}
		querySQL += "		AND (" + strings.Join(clauses, " OR ") + ")\n"
	}
	querySQL += `
		AND (
			? = ''
			OR lower(ce.name) > ?
			OR (lower(ce.name) = ? AND ce.id > ?)
		)
	ORDER BY lower(ce.name), ce.id
	LIMIT ?
	`
	args = append(args, cursor.Name, cursor.Name, cursor.Name, cursor.ID, limit)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return SearchResult{}, err
	}
	defer rows.Close()

	items := []SearchItem{}
	for rows.Next() {
		var item SearchItem
		var kind string
		var modifiedAt string
		if err := rows.Scan(
			&item.ID,
			&item.Source,
			&item.MountID,
			&item.RelativePath,
			&item.Name,
			&kind,
			&item.PreviewKind,
			&item.SizeBytes,
			&modifiedAt,
			&item.IdentityFingerprint,
		); err != nil {
			return SearchResult{}, err
		}
		item.Kind = EntryKind(kind)
		item.ModifiedAt, err = time.Parse(time.RFC3339Nano, modifiedAt)
		if err != nil {
			return SearchResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SearchResult{}, err
	}

	nextCursor := ""
	if len(items) == limit {
		last := items[len(items)-1]
		nextCursor = encodeCursor(searchCursor{Name: strings.ToLower(last.Name), ID: last.ID})
	}

	return SearchResult{
		Items:          items,
		ExcludedMounts: excluded,
		NextCursor:     nextCursor,
	}, nil
}

func (s Service) excludedMounts(ctx context.Context, boundaries []SearchBoundary) ([]ExcludedMount, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, display_name, status, index_enabled, COALESCE(mount_identity_json, '')
FROM mounts
WHERE status <> 'deleted'
ORDER BY display_name, id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	excluded := []ExcludedMount{}
	allowed := make(map[string]bool, len(boundaries))
	for _, boundary := range boundaries {
		allowed[boundary.MountID] = true
	}
	for rows.Next() {
		var mountID, name, status, identity string
		var indexEnabled int
		if err := rows.Scan(&mountID, &name, &status, &indexEnabled, &identity); err != nil {
			return nil, err
		}
		if len(allowed) > 0 && !allowed[mountID] {
			continue
		}
		reason := exclusionReason(status, indexEnabled == 1, identity != "")
		if reason == "" {
			continue
		}
		excluded = append(excluded, ExcludedMount{
			MountID: mountID,
			Name:    name,
			Reason:  reason,
		})
	}
	return excluded, rows.Err()
}

func exclusionReason(status string, indexEnabled, identityVerified bool) string {
	switch status {
	case MountStatusActive:
		if !indexEnabled {
			return "index_disabled"
		}
		if !identityVerified {
			return "mount_identity_unverifiable"
		}
		return ""
	case MountStatusPending:
		return "index_incomplete"
	case MountStatusDisabled:
		return "mount_disabled"
	case MountStatusUnavailable:
		return "mount_unavailable"
	default:
		return "mount_" + status
	}
}

func validateMount(mount Mount) error {
	if strings.TrimSpace(mount.ID) == "" || strings.TrimSpace(mount.Root) == "" ||
		mount.Source != contentref.SourcePersonal && mount.Source != contentref.SourceCommonMount {
		return ErrInvalidMount
	}
	if mount.Status != MountStatusActive || !mount.IndexEnabled || !mount.IdentityVerified {
		return ErrMountExcluded
	}
	info, err := os.Lstat(mount.Root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidMount
	}
	return nil
}

func collectBatch(ctx context.Context, mount Mount, cursor string, limit int) ([]Entry, bool, error) {
	entries := make([]Entry, 0, limit)
	complete := true
	var walk func(string) error
	walk = func(relativeDir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(entries) >= limit {
			complete = false
			return nil
		}

		directoryPath := filepath.Join(mount.Root, filepath.FromSlash(relativeDir))
		if relativeDir == "." {
			directoryPath = mount.Root
		}
		dirEntries, err := os.ReadDir(directoryPath)
		if err != nil {
			return err
		}
		sort.Slice(dirEntries, func(i, j int) bool {
			return dirEntries[i].Name() < dirEntries[j].Name()
		})

		for _, dirEntry := range dirEntries {
			if len(entries) >= limit {
				complete = false
				return nil
			}
			name := dirEntry.Name()
			relativePath := joinRelativePath(relativeDir, name)
			if _, err := storage.CleanRelativePath(relativePath); err != nil {
				continue
			}

			info, err := os.Lstat(filepath.Join(directoryPath, name))
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}

			kind, ok := entryKind(info)
			if !ok {
				continue
			}
			if relativePath > cursor {
				entries = append(entries, newEntry(mount, relativePath, name, kind, info))
			}
			if kind == EntryKindDirectory {
				if err := walk(relativePath); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk("."); err != nil {
		return nil, false, err
	}
	return entries, complete, nil
}

func newEntry(mount Mount, relativePath, name string, kind EntryKind, info os.FileInfo) Entry {
	size := info.Size()
	if kind == EntryKindDirectory {
		size = 0
	}
	return Entry{
		ID:                  stableID(mount.ID, relativePath),
		Source:              mount.Source,
		MountID:             mount.ID,
		RelativePath:        relativePath,
		Name:                name,
		Kind:                kind,
		PreviewKind:         classifyPreviewKind(name, kind),
		SizeBytes:           size,
		ModifiedAt:          info.ModTime().UTC(),
		IdentityFingerprint: identityFingerprint(kind, info),
	}
}

func normalizeBatchSize(size int) int {
	if size <= 0 {
		return DefaultBatchSize
	}
	if size < MinBatchSize {
		return MinBatchSize
	}
	if size > MaxBatchSize {
		return MaxBatchSize
	}
	return size
}

func normalizeSearchLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizeSearchBoundaries(boundaries []SearchBoundary) ([]SearchBoundary, error) {
	if len(boundaries) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(boundaries))
	normalized := make([]SearchBoundary, 0, len(boundaries))
	for _, boundary := range boundaries {
		mountID := strings.TrimSpace(boundary.MountID)
		relativePath := strings.TrimSpace(boundary.RelativePath)
		if mountID == "" {
			return nil, errors.New("boundary mount id is required")
		}
		if relativePath == "." {
			relativePath = ""
		}
		if relativePath != "" {
			cleaned, err := storage.CleanRelativePath(relativePath)
			if err != nil {
				return nil, err
			}
			relativePath = cleaned
		}
		key := mountID + "\x00" + relativePath
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, SearchBoundary{MountID: mountID, RelativePath: relativePath})
	}
	return normalized, nil
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
		return EntryKindDirectory, true
	case info.Mode().IsRegular():
		return EntryKindFile, true
	default:
		return "", false
	}
}

func classifyPreviewKind(name string, kind EntryKind) string {
	if kind != EntryKindFile {
		return "unknown_download"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".tif", ".tiff":
		return "image"
	case ".pdf":
		return "pdf"
	case ".md", ".markdown":
		return "markdown"
	case ".txt", ".text", ".log", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml":
		return "text"
	case ".mp3", ".m4a", ".ogg", ".wav", ".flac", ".aac", ".mp4", ".m4v", ".mov", ".webm", ".ogv":
		return "media"
	case ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp", ".rtf":
		return "office_download"
	default:
		return "unknown_download"
	}
}

func stableID(mountID, relativePath string) string {
	sum := sha256.Sum256([]byte(mountID + "\x00" + relativePath))
	return "cat_" + hex.EncodeToString(sum[:16])
}

func identityFingerprint(kind EntryKind, info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%s:%d:%d:%d:%d:%d", kind, stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano(), info.Mode().Perm())
	}
	return fmt.Sprintf("%s:%d:%d:%d", kind, info.Size(), info.ModTime().UnixNano(), info.Mode().Perm())
}

func escapeLike(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\\', '%', '_':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

type searchCursor struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func encodeCursor(cursor searchCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string) (searchCursor, error) {
	if strings.TrimSpace(value) == "" {
		return searchCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return searchCursor{}, fmt.Errorf("invalid search cursor: %w", err)
	}
	var cursor searchCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return searchCursor{}, fmt.Errorf("invalid search cursor: %w", err)
	}
	if cursor.Name == "" || cursor.ID == "" {
		return searchCursor{}, errors.New("invalid search cursor")
	}
	cursor.Name = strings.ToLower(cursor.Name)
	return cursor, nil
}
