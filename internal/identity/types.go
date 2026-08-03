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
)

type Account struct {
	ID           string
	Email        string
	DisplayName  string
	Role         domain.AccountRole
	Status       string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	ID         string
	AccountID  string
	TokenHash  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
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

type AccountWithPersonalSpace struct {
	Account       Account
	PersonalSpace Space
}

type SessionRequest struct {
	AccountID string
	TTL       time.Duration
}

type SessionToken struct {
	Token   string
	Session Session
}
