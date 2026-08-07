// Package membershare contains authenticated member share management. Public
// visitor exchange and session verification remain in internal/share.
package membershare

import (
	"time"

	"omnora/internal/access"
)

// Share is the member-safe representation of a share. It deliberately has no
// secret, fragment, password hash, or other capability material.
type Share struct {
	ID                 string     `json:"id"`
	PublicID           string     `json:"publicId"`
	SpaceID            string     `json:"spaceId"`
	SpaceName          string     `json:"spaceName,omitempty"`
	MountID            string     `json:"mountId"`
	MountName          string     `json:"mountName,omitempty"`
	RelativePath       string     `json:"relativePath"`
	CreatorAccountID   string     `json:"creatorAccountId,omitempty"`
	CreatorEmail       string     `json:"creatorEmail,omitempty"`
	CreatorDisplayName string     `json:"creatorDisplayName,omitempty"`
	AllowPreview       bool       `json:"allowPreview"`
	AllowDownload      bool       `json:"allowDownload"`
	MaxVisits          *int64     `json:"maxVisits,omitempty"`
	UsedVisits         int64      `json:"usedVisits"`
	MaxDownloads       *int64     `json:"maxDownloads,omitempty"`
	UsedDownloads      int64      `json:"usedDownloads"`
	ExpiresAt          time.Time  `json:"expiresAt"`
	RevokedAt          *time.Time `json:"revokedAt,omitempty"`
	Status             string     `json:"status"`
}

// IssuedShare contains the only response that may carry a newly-created
// share capability. Secret and fragment values are never part of Share/List.
type IssuedShare struct {
	Share
	Secret   string `json:"secret"`
	Fragment string `json:"fragment"`
	URL      string `json:"url"`
}

// ListFilter controls authenticated member share listing.
type ListFilter struct {
	SpaceID string
	Limit   int
}

// CreateRequest describes one share target and its public-link options. A nil
// preview/download flag uses the safe product default (enabled).
type CreateRequest struct {
	Locator       access.Locator
	Password      string
	AllowPreview  *bool
	AllowDownload *bool
	MaxVisits     *int64
	MaxDownloads  *int64
	ExpiresAt     time.Time
}
