package identity

import (
	"errors"
	"time"

	"omnora/internal/domain"
)

var (
	ErrInvalidInput              = errors.New("identity: invalid input")
	ErrAccountExists             = errors.New("identity: account already exists")
	ErrAlreadyInitialized        = errors.New("identity: already initialized")
	ErrInitializationUnavailable = errors.New("identity: initialization unavailable")
	ErrInvalidCredential         = errors.New("identity: invalid credential")
	ErrSessionInvalid            = errors.New("identity: invalid session")
	ErrSessionPurposeInvariant   = errors.New("identity: session purpose invariant failed")
	ErrEnrollmentSession         = errors.New("identity: enrollment session cannot perform this operation")
)

// DefaultSessionEntry is applied to sessions created without an explicit
// entry. The column remains for compatibility with existing databases.
const DefaultSessionEntry = "http"

type SessionPurpose string

const (
	SessionPurposeFull           SessionPurpose = "full"
	SessionPurposeTOTPEnrollment SessionPurpose = "totp_enrollment"
)

// RecentReauthenticationTTL bounds the elevated reauthentication marker. It
// is shorter than the absolute session lifetime and never extends expiry.
const RecentReauthenticationTTL = 5 * time.Minute

func (p SessionPurpose) Valid() bool {
	return p == SessionPurposeFull || p == SessionPurposeTOTPEnrollment
}

type Account struct {
	ID                    string
	Email                 string
	DisplayName           string
	Role                  domain.AccountRole
	Status                string
	PasswordHash          string
	TOTPRequired          bool
	TOTPConfirmedAt       time.Time
	PasswordResetRequired bool
	TOTPResetRequired     bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Space struct {
	ID             string
	Kind           string
	Name           string
	OwnerAccountID string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Session struct {
	ID                   string
	AccountID            string
	TokenHash            string
	Entry                string
	Purpose              SessionPurpose
	ReauthenticatedAt    time.Time
	CredentialGeneration int64
	CreatedAt            time.Time
	ExpiresAt            time.Time
	LastUsedAt           time.Time
	RevokedAt            time.Time
}

type InitializationSecret struct {
	Token     string
	TokenHash string
	ExpiresAt time.Time
}

type InitializationRequest struct {
	Token       string
	Email       string
	DisplayName string
	Password    string
}

type CreateAccountRequest struct {
	Email       string
	DisplayName string
	Password    string
	Role        domain.AccountRole
}

type AccountWithPersonalDirectory struct {
	Account           Account
	PersonalDirectory PersonalDirectory
}

type PersonalDirectory struct {
	AccountID    string `json:"accountId"`
	RelativePath string `json:"relativePath"`
	State        string `json:"state"`
}

type SessionRequest struct {
	AccountID string
	TTL       time.Duration
	Entry     string
	Purpose   SessionPurpose
}

type SessionToken struct {
	Token   string
	Session Session
}
