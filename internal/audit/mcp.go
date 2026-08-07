package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MCPEvent is the non-secret context recorded for one MCP tool invocation.
// MetadataJSON is subject to the stricter MCP metadata sanitizer.
type MCPEvent struct {
	AccountID          string
	CredentialPublicID string
	ToolName           string
	Result             string
	RequestID          string
	TraceID            string
	TargetType         string
	TargetID           string
	MetadataJSON       string
}

const (
	mcpAuditAction       = "mcp_tool"
	mcpIntentResult      = "intent"
	mcpSucceededResult   = "succeeded"
	mcpFailedResult      = "failed"
	mcpDeclinedResult    = "declined"
	mcpAuditRiskKey      = "mcp_audit_risk"
	mcpAuditWriteFailure = "audit_write_failed"
)

// RecordMCPIntent durably records an operation intent and returns its audit
// event ID. Callers must stop the operation when this method returns an error.
func (r Recorder) RecordMCPIntent(ctx context.Context, event MCPEvent) (int64, error) {
	if err := validateMCPEvent(event, true); err != nil {
		return 0, err
	}
	metadata, err := normalizeMCPMetadataJSON(event.MetadataJSON)
	if err != nil {
		return 0, err
	}
	return r.insertMCPEvent(ctx, event, mcpIntentResult, 0, metadata)
}

// RecordMCPOutcome records a terminal result and links it to its intent.
func (r Recorder) RecordMCPOutcome(ctx context.Context, intentID int64, event MCPEvent) error {
	if intentID <= 0 {
		return errors.New("MCP intent ID is required")
	}
	if r.db == nil {
		return errors.New("audit database is nil")
	}
	if err := validateMCPEvent(event, false); err != nil {
		return err
	}
	metadata, err := normalizeMCPMetadataJSON(event.MetadataJSON)
	if err != nil {
		return err
	}
	var routeGroup, result sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT route_group, result FROM audit_events WHERE id = ?`, intentID).Scan(&routeGroup, &result); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("MCP intent %d not found", intentID)
		}
		return err
	}
	if !routeGroup.Valid || routeGroup.String != "mcp" || !result.Valid || result.String != mcpIntentResult {
		return fmt.Errorf("audit event %d is not an MCP intent", intentID)
	}
	_, err = r.insertMCPEvent(ctx, event, event.Result, intentID, metadata)
	return err
}

// MarkMCPReadinessRisk records a sanitized reason and timestamp after an
// outcome audit write fails. The original error is intentionally discarded.
func (r Recorder) MarkMCPReadinessRisk(ctx context.Context, cause error) error {
	if r.db == nil {
		return errors.New("audit database is nil")
	}
	reason := mcpAuditWriteFailure
	if errors.Is(cause, context.Canceled) {
		reason = "audit_write_canceled"
	} else if errors.Is(cause, context.DeadlineExceeded) {
		reason = "audit_write_timeout"
	}
	payload, err := json.Marshal(map[string]string{
		"reason":    reason,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO system_state(key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`, mcpAuditRiskKey, string(payload))
	return err
}

func validateMCPEvent(event MCPEvent, intent bool) error {
	if strings.TrimSpace(event.ToolName) == "" {
		return errors.New("MCP tool name is required")
	}
	if strings.TrimSpace(event.TargetType) == "" {
		return errors.New("MCP target type is required")
	}
	if intent {
		return nil
	}
	switch event.Result {
	case mcpSucceededResult, mcpFailedResult, mcpDeclinedResult:
		return nil
	default:
		return fmt.Errorf("invalid MCP outcome result %q", event.Result)
	}
}

func (r Recorder) insertMCPEvent(ctx context.Context, event MCPEvent, result string, parentID int64, metadata string) (int64, error) {
	if r.db == nil {
		return 0, errors.New("audit database is nil")
	}
	var parent any
	if parentID > 0 {
		parent = parentID
	}
	var account any
	if strings.TrimSpace(event.AccountID) != "" {
		account = event.AccountID
	}
	var credential any
	if strings.TrimSpace(event.CredentialPublicID) != "" {
		credential = event.CredentialPublicID
	}
	var requestID any
	if strings.TrimSpace(event.RequestID) != "" {
		requestID = event.RequestID
	}
	var traceID any
	if strings.TrimSpace(event.TraceID) != "" {
		traceID = event.TraceID
	}
	var targetID any
	if strings.TrimSpace(event.TargetID) != "" {
		targetID = event.TargetID
	}
	resultRow, err := r.db.ExecContext(ctx, `
INSERT INTO audit_events(actor_account_id, route_group, action, target_type, target_id, metadata_json, credential_public_id, tool_name, result, request_id, trace_id, parent_event_id)
VALUES (?, 'mcp', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, account, mcpAuditAction, event.TargetType, targetID, metadata, credential, event.ToolName, result, requestID, traceID, parent)
	if err != nil {
		return 0, err
	}
	return resultRow.LastInsertId()
}
