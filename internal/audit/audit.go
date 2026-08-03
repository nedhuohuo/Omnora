package audit

import (
	"context"
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

var ErrSensitiveMetadata = errors.New("audit metadata contains sensitive data")

type Event struct {
	ActorAccountID string
	RouteGroup     domain.RouteGroup
	Action         string
	TargetType     string
	TargetID       string
	MetadataJSON   string
	IPHash         string
	UserAgentHash  string
}

type Recorder struct {
	db *sql.DB
}

func NewRecorder(db *sql.DB) Recorder {
	return Recorder{db: db}
}

func (r Recorder) Record(ctx context.Context, event Event) error {
	if r.db == nil {
		return errors.New("audit database is nil")
	}
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

	_, err = r.db.ExecContext(ctx, `
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

func HashForAudit(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
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
