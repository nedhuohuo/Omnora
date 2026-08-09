// Package memberfiles contains the shared, member-facing file application
// service used by REST and MCP adapters. Inputs are always mount-relative
// locators; host filesystem paths are deliberately absent from its API.
package memberfiles

import (
	"time"

	"omnora/internal/catalog"
	"omnora/internal/contentref"
	"omnora/internal/domain"
)

// Mount is a sanitized common-mount grant. The protected personal-default
// mount is represented by the personal content source and is never listed.
type Mount struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	StorageKind domain.StorageKind       `json:"storageKind"`
	Permission  domain.ContentPermission `json:"permission"`
	Mode        domain.MountMode         `json:"mode"`
	ReadOnly    bool                     `json:"readOnly"`
}

type SearchRequest struct {
	Source  contentref.Source `json:"source"`
	MountID string            `json:"mountId,omitempty"`
	Query   string            `json:"query"`
	Limit   int               `json:"limit,omitempty"`
	Cursor  string            `json:"cursor,omitempty"`
}

// SearchResult contains only catalog rows that remain inside current member
// ACLs and token boundaries.
type SearchResult struct {
	Items          []catalog.SearchItem    `json:"items"`
	ExcludedMounts []catalog.ExcludedMount `json:"excludedMounts,omitempty"`
	NextCursor     string                  `json:"nextCursor,omitempty"`
}

// TextResult is a bounded text response. Size is the current regular-file
// size and BytesRead is the number of bytes returned in Text.
type TextResult struct {
	Text      string `json:"text"`
	Size      int64  `json:"size"`
	BytesRead int64  `json:"bytesRead"`
	Truncated bool   `json:"truncated"`
}

// Option configures a MemberFileService. It is kept deliberately small so
// authorization remains live and cannot accidentally be cached.
type Option func(*Service)

// WithClock supplies a clock for callers that need deterministic metadata
// tests. The read service does not cache authorization decisions.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}
