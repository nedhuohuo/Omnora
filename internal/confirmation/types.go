package confirmation

import (
	"encoding/json"
	"time"
)

// Impact is the user-visible summary of a high-risk operation. Details must
// contain sanitized metadata only; secrets, host paths, and file content do
// not belong in a confirmation challenge.
type Impact struct {
	Summary   string         `json:"summary"`
	ItemCount int            `json:"itemCount"`
	TotalSize int64          `json:"totalSize"`
	Details   map[string]any `json:"details,omitempty"`
}

// Preview is the exact operation preview bound to a one-time challenge.
type Preview struct {
	ToolName          string
	Args              json.RawMessage
	ObjectFingerprint string
	Impact            Impact
}

// Challenge is returned by Begin. RequestState is an opaque public-id.secret
// value and is intentionally never persisted in its plaintext form.
type Challenge struct {
	RequestState string
	Impact       Impact
	ExpiresAt    time.Time
}

// ConsumeRequest contains the exact operation retry and the explicit human
// decision. Decision must be either "accept" or "decline".
type ConsumeRequest struct {
	RequestState      string
	ToolName          string
	Args              json.RawMessage
	ObjectFingerprint string
	Decision          string
}
