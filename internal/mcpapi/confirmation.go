package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/aitoken"
	"omnora/internal/audit"
	"omnora/internal/confirmation"
	"omnora/internal/domain"
)

var (
	ErrClientCapability     = errors.New("client_capability_required")
	ErrConfirmationInvalid  = errors.New("confirmation_invalid")
	ErrConfirmationDeclined = errors.New("confirmation_declined")
	ErrConfirmationStale    = errors.New("confirmation_stale")
)

func reliableFormElicitation(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	return reliableFormCapabilities(req.ClientCapabilities())
}

func reliableFormCapabilities(caps *mcp.ClientCapabilities) bool {
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	return caps.Elicitation.Form != nil || (caps.Elicitation.Form == nil && caps.Elicitation.URL == nil)
}

func rawArguments(req *mcp.CallToolRequest) json.RawMessage {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), req.Params.Arguments...)
}

// requireConfirmation persists and consumes one confirmation challenge. It
// never treats a boolean in normal tool arguments as a decision.
func (d ToolDependencies) requireConfirmation(ctx context.Context, req *mcp.CallToolRequest, principal aitoken.Principal, tool string, preview func() (confirmation.Preview, error)) (bool, *mcp.CallToolResult, error) {
	if !reliableFormElicitation(req) {
		return false, nil, ErrClientCapability
	}
	if d.Confirmations == nil {
		return false, nil, confirmation.ErrUnavailable
	}
	args := rawArguments(req)
	responses := req.Params.InputResponses
	if req.Params.RequestState == "" && len(responses) == 0 {
		current, err := preview()
		if err != nil {
			return false, nil, err
		}
		current.ToolName = tool
		current.Args = args
		challenge, err := d.Confirmations.Begin(ctx, principal, current)
		if err != nil {
			return false, nil, err
		}
		publicID, _, _ := strings.Cut(challenge.RequestState, ".")
		d.recordConfirmationAudit(ctx, req, principal, tool, "confirmation_requested", challenge.Impact, "pending", publicID, challenge.ExpiresAt)
		return false, &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{
				Mode: "form", Message: challenge.Impact.Summary,
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"confirmed": map[string]any{"type": "boolean"}}, "required": []string{"confirmed"}},
			}},
			RequestState: challenge.RequestState,
		}, nil
	}
	if req.Params.RequestState == "" || len(responses) == 0 {
		return false, nil, ErrConfirmationInvalid
	}
	response, ok := responses["confirm"].(*mcp.ElicitResult)
	if !ok || response == nil {
		return false, nil, ErrConfirmationInvalid
	}
	decision := response.Action
	confirmed := false
	if response.Content != nil {
		confirmed, _ = response.Content["confirmed"].(bool)
	}
	if decision != "accept" && decision != "decline" && decision != "cancel" {
		return false, nil, ErrConfirmationInvalid
	}
	if decision == "cancel" || decision == "decline" || !confirmed {
		decision = "decline"
	}
	current, err := preview()
	if err != nil {
		if errors.Is(err, confirmation.ErrUnavailable) {
			return false, nil, ErrConfirmationStale
		}
		return false, nil, err
	}
	err = d.Confirmations.Consume(ctx, principal, confirmation.ConsumeRequest{RequestState: req.Params.RequestState, ToolName: tool, Args: args, ObjectFingerprint: current.ObjectFingerprint, Decision: decision})
	if errors.Is(err, confirmation.ErrDeclined) {
		publicID, _, _ := strings.Cut(req.Params.RequestState, ".")
		d.recordConfirmationAudit(ctx, req, principal, tool, "confirmation_declined", current.Impact, "declined", publicID, time.Time{})
		return false, nil, ErrConfirmationDeclined
	}
	if errors.Is(err, confirmation.ErrUnavailable) {
		return false, nil, ErrConfirmationStale
	}
	if err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func (d ToolDependencies) recordConfirmationAudit(ctx context.Context, req *mcp.CallToolRequest, principal aitoken.Principal, tool, action string, impact confirmation.Impact, result, publicID string, expiresAt time.Time) {
	if !d.AuditEnabled {
		return
	}
	requestID := ""
	if req != nil && req.Extra != nil {
		requestID = headerValue(req.Extra.Header, "X-Request-ID")
	}
	metadataMap := map[string]any{"summary": impact.Summary, "itemCount": impact.ItemCount, "totalSize": impact.TotalSize, "status": result, "confirmationId": publicID}
	if !expiresAt.IsZero() {
		metadataMap["expiresAt"] = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	if requestID != "" {
		metadataMap["requestId"] = requestID
	}
	if req != nil && req.Extra != nil {
		if traceID := headerValue(req.Extra.Header, "X-Trace-ID"); traceID != "" {
			metadataMap["traceId"] = traceID
		}
	}
	metadata, _ := json.Marshal(metadataMap)
	err := d.AuditRecorder.Record(ctx, audit.Event{ActorAccountID: principal.AccountID, RouteGroup: domain.RouteGroupMCP, Action: action, TargetType: "mcp_confirmation", TargetID: strings.TrimSpace(tool), MetadataJSON: string(metadata)})
	if err != nil {
		_ = d.AuditRecorder.MarkReadinessRisk(ctx, err)
	}
	_ = requestID // request ID is carried by the MCP audit columns for tool intent/outcome.
}

func impactFor(summary string, itemCount int, totalSize int64, details map[string]any, fingerprint string) confirmation.Preview {
	return confirmation.Preview{ObjectFingerprint: fingerprint, Impact: confirmation.Impact{Summary: summary, ItemCount: itemCount, TotalSize: totalSize, Details: details}}
}
