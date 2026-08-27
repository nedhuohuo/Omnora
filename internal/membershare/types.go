// Package membershare contains authenticated member share management. Public
// visitor exchange and session verification remain in internal/share.
package membershare

import (
	"time"

	"omnora/internal/access"
	"omnora/internal/contentref"
)

// Share is safe for member and MCP output. Personal targets deliberately omit
// both the protected default-mount ID and the account storage prefix.
type Share struct {
	ID                 string            `json:"id"`
	PublicID           string            `json:"publicId"`
	Source             contentref.Source `json:"source"`
	MountID            string            `json:"mountId,omitempty"`
	MountName          string            `json:"mountName,omitempty"`
	RelativePath       string            `json:"relativePath"`
	CreatorAccountID   string            `json:"creatorAccountId,omitempty"`
	CreatorEmail       string            `json:"creatorEmail,omitempty"`
	CreatorDisplayName string            `json:"creatorDisplayName,omitempty"`
	AllowPreview       bool              `json:"allowPreview"`
	AllowDownload      bool              `json:"allowDownload"`
	MaxVisits          *int64            `json:"maxVisits,omitempty"`
	UsedVisits         int64             `json:"usedVisits"`
	MaxDownloads       *int64            `json:"maxDownloads,omitempty"`
	UsedDownloads      int64             `json:"usedDownloads"`
	ExpiresAt          time.Time         `json:"expiresAt"`
	RevokedAt          *time.Time        `json:"revokedAt,omitempty"`
	Status             string            `json:"status"`
}

type IssuedShare struct {
	Share
	Secret   string `json:"secret"`
	Fragment string `json:"fragment"`
	URL      string `json:"url"`
}

type ListFilter struct {
	MountID string
	Limit   int
}

type CreateRequest struct {
	Locator       access.Locator
	Password      string
	AllowPreview  *bool
	AllowDownload *bool
	MaxVisits     *int64
	MaxDownloads  *int64
	ExpiresAt     time.Time
}
