package transferticket

import (
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
)

// Operation describes the only two transfer streams exposed by a ticket.
type Operation string

const (
	OperationDownload Operation = "download"
	OperationUpload   Operation = "upload"
)

// Status is the persisted lifecycle of a transfer ticket.
type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusCanceled  Status = "canceled"
	StatusExpired   Status = "expired"
)

// IssuedTicket deliberately separates the public URL and the secret. The
// secret is intended for an Authorization header and is never placed in URL.
type IssuedTicket struct {
	ID                string
	TicketID          string
	PublicID          string
	Secret            string
	BearerToken       string
	URL               string
	TicketURL         string
	Operation         Operation
	RequiredScope     aitoken.Scope
	Locator           access.Locator
	ObjectFingerprint string
	Size              int64
	MaxBytes          int64
	ExpiresAt         time.Time
}

// VerifiedTicket is a live, authorization-checked ticket. Mount and
// Principal are included so HTTP transfer handlers can stream without
// reimplementing authorization or leaking host paths into request input.
type VerifiedTicket struct {
	ID                string
	TicketID          string
	PublicID          string
	Operation         Operation
	RequiredScope     aitoken.Scope
	AccountID         string
	TokenID           string
	Principal         aitoken.Principal
	Locator           access.Locator
	Mount             access.AuthorizedMount
	UploadID          string
	ObjectFingerprint string
	MaxBytes          int64
	ConsumedBytes     int64
	Status            Status
	ExpiresAt         time.Time
}
