package aitoken

import (
	"errors"
	"time"

	"omnora/internal/contentref"
)

type Scope string

const (
	ScopeMountsRead          Scope = "mounts:read"
	ScopeFilesList           Scope = "files:list"
	ScopeFilesMetadata       Scope = "files:metadata"
	ScopeFilesText           Scope = "files:text"
	ScopeFilesDownloadTicket Scope = "files:download_ticket"
	ScopeSearchRead          Scope = "search:read"
	ScopeUploadsCreate       Scope = "uploads:create"
	ScopeFilesWrite          Scope = "files:write"
	ScopeFilesTrash          Scope = "files:trash"
	ScopeTrashRead           Scope = "trash:read"
	ScopeFilesRestore        Scope = "files:restore"
	ScopeFilesPurge          Scope = "files:purge"
	ScopeSharesRead          Scope = "shares:read"
	ScopeSharesCreate        Scope = "shares:create"
	ScopeSharesRevoke        Scope = "shares:revoke"
)

const SourceAllAccountContent contentref.Source = "all_account_content"

var (
	ErrInvalidInput = errors.New("aitoken: invalid input")
	ErrInvalidScope = errors.New("aitoken: invalid scope")
	ErrInvalidToken = errors.New("aitoken: invalid token")
)

type DirectoryBoundary struct {
	Source       contentref.Source
	MountID      string
	RelativePath string
}

type Token struct {
	ID                   string
	PublicID             string
	SecretHash           string
	AccountID            string
	Name                 string
	Scopes               []Scope
	Boundaries           []DirectoryBoundary
	CredentialGeneration int64
	CreatedAt            time.Time
	ExpiresAt            time.Time
	LastUsedAt           time.Time
	RevokedAt            time.Time
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
