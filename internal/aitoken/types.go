package aitoken

import (
	"errors"
	"time"

	"omnora/internal/domain"
)

type Scope string

const (
	ScopeSpacesRead          Scope = "spaces:read"
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

var (
	ErrInvalidInput = errors.New("aitoken: invalid input")
	ErrInvalidScope = errors.New("aitoken: invalid scope")
	ErrInvalidToken = errors.New("aitoken: invalid token")
)

type AuthorizationModel string

const (
	AuthorizationModelLegacyBoundary AuthorizationModel = "legacy_boundary"
	AuthorizationModelSpaceGrant     AuthorizationModel = "space_grant"
)

type FolderDenyStatus string

const (
	FolderDenyStatusActive      FolderDenyStatus = "active"
	FolderDenyStatusNeedsReview FolderDenyStatus = "needs_review"
)

type DirectoryBoundary struct {
	SpaceID      string
	MountID      string
	RelativePath string
}

type FolderDeny struct {
	MountID             string
	RelativePath        string
	IdentityFingerprint string
	Status              FolderDenyStatus
}

type SpaceGrant struct {
	SpaceID        string
	Permission     domain.SpacePermission
	FolderDenylist []FolderDeny
}

type Token struct {
	ID                   string
	PublicID             string
	SecretHash           string
	AccountID            string
	Name                 string
	Scopes               []Scope
	AuthorizationModel   AuthorizationModel
	Boundaries           []DirectoryBoundary
	SpaceGrants          []SpaceGrant
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

type CreateSpaceGrantRequest struct {
	AccountID   string
	Name        string
	Scopes      []Scope
	SpaceGrants []SpaceGrant
	ExpiresAt   time.Time
}

type UpdateRequest struct {
	AccountID   string
	TokenID     string
	Name        string
	Scopes      []Scope
	SpaceGrants []SpaceGrant
	ExpiresAt   time.Time
}

type Principal struct {
	AccountID          string
	TokenID            string
	PublicID           string
	Scopes             []Scope
	AuthorizationModel AuthorizationModel
	Boundaries         []DirectoryBoundary
	SpaceGrants        []SpaceGrant
	ExpiresAt          time.Time
	LastUsedAt         time.Time
}

func (p Principal) HasScope(scope Scope) bool {
	for _, candidate := range p.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}
