package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"omnora/internal/domain"
)

var (
	ErrSensitiveMetadata = errors.New("audit metadata contains sensitive data")
	ErrWriteFailed       = errors.New("audit write failed")
)

const ReadinessRiskKey = "audit_write_risk"

// Domain labels keep the same input in separate audit namespaces.  A client
// address must not be usable as a subject identifier (or vice versa), even
// when an operator can query the audit database.
const (
	HashDomainClientIP  = "client_ip"
	HashDomainUserAgent = "user_agent"
	HashDomainSubject   = "subject"
)

type Event struct {
	ActorAccountID     string
	RouteGroup         domain.RouteGroup
	Action             string
	TargetType         string
	TargetID           string
	MetadataJSON       string
	IPHash             string
	UserAgentHash      string
	CredentialPublicID string
	ToolName           string
	Result             string
	ReasonCode         string
	RequestID          string
	TraceID            string
	ParentEventID      *int64
	SubjectHash        string
}

type Recorder struct {
	db *sql.DB
}

type execContexter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func NewRecorder(db *sql.DB) Recorder {
	return Recorder{db: db}
}

func (r Recorder) Record(ctx context.Context, event Event) error {
	if r.db == nil {
		return errors.New("audit database is nil")
	}
	return record(ctx, r.db, event)
}

// RecordTx writes an audit event using the caller's transaction. High-risk
// mutations should call this before committing so the state change and its
// success audit row are atomic.
func (r Recorder) RecordTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if tx == nil {
		return errors.New("audit transaction is nil")
	}
	return record(ctx, tx, event)
}

// MarkReadinessRisk records a durable, non-sensitive audit failure marker.
// Callers must stop treating the process as ready until the marker is cleared
// by an operator or a successful recovery gate.
func (r Recorder) MarkReadinessRisk(ctx context.Context, cause error) error {
	if r.db == nil {
		return errors.New("audit database is nil")
	}
	reason := "audit_write_failed"
	if errors.Is(cause, context.Canceled) {
		reason = "audit_write_canceled"
	} else if errors.Is(cause, context.DeadlineExceeded) {
		reason = "audit_write_timeout"
	}
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO system_state(key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`, ReadinessRiskKey, string(payload))
	return err
}

// ClearReadinessRisk removes the durable audit failure marker after the audit
// path is healthy again or an operator has completed recovery.
func (r Recorder) ClearReadinessRisk(ctx context.Context) error {
	if r.db == nil {
		return errors.New("audit database is nil")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM system_state WHERE key = ?`, ReadinessRiskKey)
	return err
}

// WrapWriteError marks a Record/RecordTx failure so HTTP handlers can set
// readiness risk without treating ordinary business errors as audit failures.
func WrapWriteError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrWriteFailed, err)
}

func record(ctx context.Context, exec execContexter, event Event) error {
	if strings.TrimSpace(event.Action) == "" {
		return errors.New("audit action is required")
	}
	if strings.TrimSpace(event.TargetType) == "" {
		return errors.New("audit target type is required")
	}
	if event.RouteGroup != "" && !event.RouteGroup.Valid() {
		return errors.New("audit route group is invalid")
	}

	metadata, err := NormalizeMetadataJSON(event.MetadataJSON)
	if err != nil {
		return err
	}

	var routeGroup any
	if event.RouteGroup != "" {
		routeGroup = string(event.RouteGroup)
	}
	var actor any
	if strings.TrimSpace(event.ActorAccountID) != "" {
		actor = event.ActorAccountID
	}
	var targetID any
	if strings.TrimSpace(event.TargetID) != "" {
		targetID = event.TargetID
	}
	var ipHash any
	if strings.TrimSpace(event.IPHash) != "" {
		ipHash = event.IPHash
	}
	var userAgentHash any
	if strings.TrimSpace(event.UserAgentHash) != "" {
		userAgentHash = event.UserAgentHash
	}

	result := strings.TrimSpace(event.Result)
	if result == "" {
		result = "success"
	}
	var credentialPublicID, toolName, reasonCode, requestID, traceID, subjectHash any
	if strings.TrimSpace(event.CredentialPublicID) != "" {
		credentialPublicID = event.CredentialPublicID
	}
	if strings.TrimSpace(event.ToolName) != "" {
		toolName = event.ToolName
	}
	if strings.TrimSpace(event.ReasonCode) != "" {
		reasonCode = event.ReasonCode
	}
	if strings.TrimSpace(event.RequestID) != "" {
		requestID = event.RequestID
	}
	if strings.TrimSpace(event.TraceID) != "" {
		traceID = event.TraceID
	}
	if strings.TrimSpace(event.SubjectHash) != "" {
		subjectHash = event.SubjectHash
	}
	var parentEventID any
	if event.ParentEventID != nil {
		parentEventID = *event.ParentEventID
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO audit_events(actor_account_id, route_group, action, target_type, target_id, ip_hash, user_agent_hash, metadata_json,
                         credential_public_id, tool_name, result, reason_code, request_id, trace_id, parent_event_id, subject_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, actor, routeGroup, event.Action, event.TargetType, targetID, ipHash, userAgentHash, metadata,
		credentialPublicID, toolName, result, reasonCode, requestID, traceID, parentEventID, subjectHash)
	if err == nil {
		return nil
	}
	// Keep the recorder usable against an expand-phase/legacy fixture while
	// production migrations expose the full contract. Never hide errors from
	// the extended insert when the table itself is otherwise complete.
	if !strings.Contains(strings.ToLower(err.Error()), "no column named") {
		return err
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO audit_events(actor_account_id, route_group, action, target_type, target_id, ip_hash, user_agent_hash, metadata_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, actor, routeGroup, event.Action, event.TargetType, targetID, ipHash, userAgentHash, metadata)
	return err
}

func MetadataFromMap(metadata map[string]any) (string, error) {
	if metadata == nil {
		return "{}", nil
	}
	if err := rejectSensitiveValue("", metadata); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func NormalizeMetadataJSON(metadataJSON string) (string, error) {
	if strings.TrimSpace(metadataJSON) == "" {
		return "{}", nil
	}
	var metadata any
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return "", fmt.Errorf("invalid audit metadata json: %w", err)
	}
	if _, ok := metadata.(map[string]any); !ok {
		return "", errors.New("audit metadata must be a json object")
	}
	if err := rejectSensitiveValue("", metadata); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// normalizeMCPMetadataJSON applies the regular audit metadata checks plus the
// stricter MCP boundary: MCP audit rows may contain labels and object IDs, but
// never bearer material, file contents, or host filesystem paths.
func normalizeMCPMetadataJSON(metadataJSON string) (string, error) {
	metadata, err := NormalizeMetadataJSON(metadataJSON)
	if err != nil {
		return "", err
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(metadata), &decoded); err != nil {
		return "", err
	}
	if err := rejectMCPMetadataValue("", decoded); err != nil {
		return "", err
	}
	return metadata, nil
}

func rejectMCPMetadataValue(key string, value any) error {
	if isSensitiveKey(key) {
		return fmt.Errorf("%w: %s", ErrSensitiveMetadata, key)
	}
	normalizedKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	compactKey := strings.ReplaceAll(normalizedKey, "_", "")
	for _, fragment := range []string{"content", "filedata", "checksum", "digest", "rootpath", "hostpath", "absolutepath", "filesystempath"} {
		if strings.Contains(compactKey, fragment) {
			return fmt.Errorf("%w: %s", ErrSensitiveMetadata, key)
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		for childKey, childValue := range typed {
			if err := rejectMCPMetadataValue(childKey, childValue); err != nil {
				return err
			}
		}
	case []any:
		for _, childValue := range typed {
			if err := rejectMCPMetadataValue(key, childValue); err != nil {
				return err
			}
		}
	case string:
		if isSensitiveString(typed) || looksLikeHostPath(typed) {
			return fmt.Errorf("%w: %s", ErrSensitiveMetadata, key)
		}
	}
	return nil
}

func looksLikeHostPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

// HashForAudit returns a stable, non-reversible identifier for audit and rate
// limiting. The key is generated and persisted by the deployment entrypoint;
// callers must never fall back to an unkeyed digest for security decisions.
// A zero key, domain, or value deliberately produces no identifier so a
// misconfigured instance fails closed instead of silently weakening privacy.
func HashForAudit(key []byte, domain, value string) string {
	domain = strings.TrimSpace(domain)
	if len(key) == 0 || domain == "" || strings.TrimSpace(value) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func RedactValue(key string, value any) any {
	if isSensitiveKey(key) || isSensitiveString(fmt.Sprint(value)) {
		return "[redacted]"
	}
	return value
}

func rejectSensitiveValue(key string, value any) error {
	if isSensitiveKey(key) {
		return fmt.Errorf("%w: %s", ErrSensitiveMetadata, key)
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, childValue := range typed {
			if err := rejectSensitiveValue(childKey, childValue); err != nil {
				return err
			}
		}
	case []any:
		for _, childValue := range typed {
			if err := rejectSensitiveValue(key, childValue); err != nil {
				return err
			}
		}
	case string:
		if isSensitiveString(typed) {
			return fmt.Errorf("%w: %s", ErrSensitiveMetadata, key)
		}
	}
	return nil
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	if normalized == "" {
		return false
	}
	sensitiveFragments := []string{
		"password",
		"passwd",
		"secret",
		"token",
		"api_key",
		"apikey",
		"access_key",
		"private_key",
		"authorization",
		"cookie",
		"session",
		"share_url",
		"shareurl",
		"share_token",
		"sharetoken",
	}
	for _, fragment := range sensitiveFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func isSensitiveString(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "bearer ") ||
		strings.HasPrefix(lower, "basic ") ||
		strings.Contains(lower, "password=") ||
		strings.Contains(lower, "token=") ||
		strings.Contains(lower, "secret=") ||
		strings.Contains(lower, "apikey=") ||
		strings.Contains(lower, "api_key=") ||
		strings.Contains(lower, "sk-") {
		return true
	}
	if strings.Contains(lower, "/share/") || strings.Contains(lower, "/shares/") || strings.Contains(lower, "/s/") {
		if parsed, err := url.Parse(lower); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return true
		}
	}
	return false
}
