package memberfiles

import (
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/catalog"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/storage"
)

const (
	DefaultReadTextBytes int64 = 64 * 1024
	MaxReadTextBytes     int64 = 1024 * 1024
)

var (
	ErrNotTextFile = errors.New("member files: object is not a UTF-8 text file")
	// ErrBinaryFile is a compatibility alias for callers that describe the
	// same stable failure as a binary-file rejection.
	ErrBinaryFile = ErrNotTextFile
)

// List returns a mount-relative directory listing after a live viewer check.
func (s *Service) List(ctx context.Context, subject access.Subject, locator access.Locator) (files.DirectoryListing, error) {
	mount, err := s.authorizeRead(ctx, subject, locator, aitoken.ScopeFilesList)
	if err != nil {
		return files.DirectoryListing{}, err
	}
	return s.files.ListDirectory(toFilesMount(mount), mount.RelativePath)
}

// Metadata returns mount-relative metadata after a live viewer check.
func (s *Service) Metadata(ctx context.Context, subject access.Subject, locator access.Locator) (files.Entry, error) {
	mount, err := s.authorizeRead(ctx, subject, locator, aitoken.ScopeFilesMetadata)
	if err != nil {
		return files.Entry{}, err
	}
	return s.files.Stat(toFilesMount(mount), mount.RelativePath)
}

// Preview returns current object identity under the caller-provided scope.
// MCP confirmation previews use this to re-check the same operation scope
// without requiring the separate files:metadata capability.
func (s *Service) Preview(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (files.Entry, error) {
	write := scope == aitoken.ScopeFilesWrite || scope == aitoken.ScopeFilesTrash || scope == aitoken.ScopeFilesPurge
	permission := domain.SpacePermissionViewer
	if scope == aitoken.ScopeSharesCreate {
		permission = domain.SpacePermissionManager
	}
	if write {
		permission = domain.SpacePermissionEditor
	}
	mount, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator, RequiredPermission: permission, Write: write})
	if err != nil {
		return files.Entry{}, err
	}
	return s.files.Stat(toFilesMount(mount), mount.RelativePath)
}

// Search delegates indexed lookup to catalog.Service, but supplies only
// currently authorized mount/path boundaries and filters the returned rows a
// second time before exposing them to a caller.
func (s *Service) Search(ctx context.Context, subject access.Subject, req SearchRequest) (SearchResult, error) {
	if err := s.validate(subject); err != nil {
		return SearchResult{}, err
	}
	subject, err := s.requireTokenScope(ctx, subject, aitoken.ScopeSearchRead)
	if err != nil {
		return SearchResult{}, err
	}
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	if req.SpaceID == "" {
		return SearchResult{}, ErrInvalidInput
	}
	records, err := s.visibleMounts(ctx, subject, req.SpaceID, aitoken.ScopeSearchRead)
	if err != nil {
		return SearchResult{}, err
	}
	if len(records) == 0 {
		return SearchResult{Items: []catalog.SearchItem{}}, nil
	}

	boundaries := make([]catalog.SearchBoundary, 0)
	authorized := make(map[string][]string, len(records))
	for _, record := range records {
		for _, boundary := range record.boundaries {
			relative := boundary
			if relative == "." {
				relative = ""
			}
			boundaries = append(boundaries, catalog.SearchBoundary{MountID: record.mount.ID, RelativePath: relative})
			authorized[record.mount.ID] = append(authorized[record.mount.ID], relative)
		}
	}
	result, err := s.catalog.Search(ctx, catalog.SearchOptions{
		SpaceID:    req.SpaceID,
		Query:      req.Query,
		Limit:      req.Limit,
		Cursor:     req.Cursor,
		Boundaries: boundaries,
	})
	if err != nil {
		return SearchResult{}, err
	}

	items := make([]catalog.SearchItem, 0, len(result.Items))
	for _, item := range result.Items {
		cleaned, cleanErr := cleanCatalogPath(item.RelativePath)
		if cleanErr != nil || item.SpaceID != req.SpaceID || !pathInBoundaries(item.MountID, cleaned, authorized) {
			continue
		}
		item.RelativePath = cleaned
		items = append(items, item)
	}
	excluded := make([]catalog.ExcludedMount, 0, len(result.ExcludedMounts))
	for _, item := range result.ExcludedMounts {
		if _, ok := authorized[item.MountID]; ok {
			excluded = append(excluded, item)
		}
	}
	return SearchResult{Items: items, ExcludedMounts: excluded, NextCursor: result.NextCursor}, nil
}

// ReadText reads at most one bounded prefix of a regular UTF-8 file. Values
// above the hard limit are clamped to the hard limit; a non-positive value
// selects the documented default.
func (s *Service) ReadText(ctx context.Context, subject access.Subject, locator access.Locator, maxBytes int64) (TextResult, error) {
	mount, err := s.authorizeRead(ctx, subject, locator, aitoken.ScopeFilesText)
	if err != nil {
		return TextResult{}, err
	}
	file, info, err := s.files.OpenFile(toFilesMount(mount), mount.RelativePath)
	if err != nil {
		return TextResult{}, err
	}
	defer file.Close()

	limit := maxBytes
	if limit <= 0 {
		limit = DefaultReadTextBytes
	}
	if limit > MaxReadTextBytes {
		limit = MaxReadTextBytes
	}
	// Keep a few bytes beyond the returned budget so a valid UTF-8 code point
	// split exactly at the boundary can be distinguished from an invalid byte
	// at the end of the prefix.
	data, err := io.ReadAll(io.LimitReader(file, limit+utf8.UTFMax))
	if err != nil {
		return TextResult{}, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		prefix := data[:limit]
		var ok bool
		prefix, ok = trimIncompleteUTF8(prefix, data)
		if !ok {
			return TextResult{}, ErrNotTextFile
		}
		data = prefix
	}
	if !validTextBytes(data) {
		return TextResult{}, ErrNotTextFile
	}
	return TextResult{
		Text:      string(data),
		Size:      info.Size(),
		BytesRead: int64(len(data)),
		Truncated: truncated,
	}, nil
}

// trimIncompleteUTF8 removes bytes only when the first invalid sequence at
// the response boundary is a valid code point whose remaining bytes are
// present in the look-ahead sample. An invalid byte such as 0xff therefore
// cannot be hidden by trimming a few suffix bytes.
func trimIncompleteUTF8(prefix, sample []byte) ([]byte, bool) {
	if utf8.Valid(prefix) {
		return prefix, true
	}
	for index := 0; index < len(prefix); {
		runeValue, size := utf8.DecodeRune(prefix[index:])
		if runeValue == utf8.RuneError && size == 1 {
			width := utf8Width(prefix[index])
			if width == 0 || len(prefix)-index >= utf8.UTFMax || index+width > len(sample) {
				return nil, false
			}
			candidate := sample[index : index+width]
			if !utf8.Valid(candidate) {
				return nil, false
			}
			return prefix[:index], true
		}
		index += size
	}
	return nil, false
}

func utf8Width(first byte) int {
	switch {
	case first < 0x80:
		return 1
	case first >= 0xc2 && first <= 0xdf:
		return 2
	case first >= 0xe0 && first <= 0xef:
		return 3
	case first >= 0xf0 && first <= 0xf4:
		return 4
	default:
		return 0
	}
}

func validTextBytes(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if b == 0 || b == 0x7f || (b < 0x09) || (b > 0x0d && b < 0x20) {
			return false
		}
	}
	return true
}

func (s *Service) authorizeRead(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	if err := s.validate(subject); err != nil {
		return access.AuthorizedMount{}, err
	}
	if strings.TrimSpace(locator.SpaceID) == "" || strings.TrimSpace(locator.MountID) == "" {
		return access.AuthorizedMount{}, ErrInvalidInput
	}
	mount, err := s.guard.Authorize(ctx, access.CheckRequest{
		Subject:            subject,
		Scope:              scope,
		Locator:            locator,
		RequiredPermission: domain.SpacePermissionViewer,
	})
	if err != nil {
		return access.AuthorizedMount{}, err
	}
	return mount, nil
}

func toFilesMount(mount access.AuthorizedMount) files.Mount {
	return files.Mount{Root: mount.Root, Mode: mount.Mode, Kind: mount.Kind}
}

func pathInBoundaries(mountID, relativePath string, boundaries map[string][]string) bool {
	cleaned, err := cleanCatalogPath(relativePath)
	if err != nil {
		return false
	}
	paths, ok := boundaries[mountID]
	if !ok {
		return false
	}
	for _, boundary := range paths {
		if boundary == "" || cleaned == boundary || strings.HasPrefix(cleaned, boundary+"/") {
			return true
		}
	}
	return false
}

// cleanCatalogPath is stricter than the general storage normalizer for
// untrusted indexed rows: an intermediate ".." must be rejected rather than
// normalized away, otherwise a row such as docs/../outside.txt could escape
// a token's docs boundary.
func cleanCatalogPath(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return "", ErrInvalidInput
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", ErrInvalidInput
		}
	}
	return storage.CleanRelativePath(raw)
}
