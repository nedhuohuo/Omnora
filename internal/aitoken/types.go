package aitoken

import (
	"errors"
	"time"
)

type Scope string

const (
	ScopeSpacesRead          Scope = "spaces:read"
	ScopeFilesList           Scope = "files:list"
	ScopeFilesMetadata       Scope = "files:metadata"
	ScopeFilesText           Scope = "files:text"
	ScopeFilesDownloadTicket Scope = "files:download_ticket"
	ScopeSearchRead          Scope = "search:read"
)

var (
	ErrInvalidInput = errors.New("aitoken: invalid input")
	ErrInvalidScope = errors.New("aitoken: invalid scope")
	ErrInvalidToken = errors.New("aitoken: invalid token")
)

type DirectoryBoundary struct {
	SpaceID      string
	MountID      string
	RelativePath string
}

type Token struct {
	ID         string
	PublicID   string
	SecretHash string
	AccountID  string
	Name       string
	Scopes     []Scope
	Boundaries []DirectoryBoundary
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
}

type IssuedToken struct {
	Token       Token
	Secret      string
	BearerToken string
}

type CreateRequest struct {
	AccountID  string
	Name       string
	Scopes     []Scope
	Boundaries []DirectoryBoundary
	ExpiresAt  time.Time
}

type Principal struct {
	AccountID  string
	TokenID    string
	PublicID   string
	Scopes     []Scope
	Boundaries []DirectoryBoundary
	ExpiresAt  time.Time
	LastUsedAt time.Time
}

func (p Principal) HasScope(scope Scope) bool {
	for _, candidate := range p.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}
