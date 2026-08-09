package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/audit"
	"omnora/internal/confirmation"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
	"omnora/internal/membershare"
	"omnora/internal/storage"
	"omnora/internal/transferticket"
)

const (
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"
)

func toolFromSpec(spec ToolSpec) *mcp.Tool {
	openWorld := spec.OpenWorld
	destructive := spec.Destructive
	idempotent := spec.ReadOnly && spec.Name != "files.prepare_download"
	return &mcp.Tool{
		Name: spec.Name, Title: spec.Title, Description: spec.Description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: spec.ReadOnly, DestructiveHint: &destructive,
			OpenWorldHint: &openWorld, IdempotentHint: idempotent,
		},
	}
}

// RegisterOrdinaryTools installs exactly the ordinary (non-confirmation)
// catalogue. It is safe to call for a fresh stateless SDK server.
func RegisterOrdinaryTools(server *mcp.Server, deps ToolDependencies) {
	if server == nil {
		return
	}
	addTool(server, OrdinaryToolSpecs()[0], func(ctx context.Context, req *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, ToolOutput[[]memberfiles.Mount], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[[]memberfiles.Mount](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[[]memberfiles.Mount](req, errors.New("member files service is unavailable"))
		}
		items, err := deps.MemberFiles.ListMounts(ctx, ps.Subject)
		if err != nil {
			return toolFailure[[]memberfiles.Mount](req, err)
		}
		return nil, ToolOutput[[]memberfiles.Mount]{Data: items}, nil
	})
	addTool(server, OrdinaryToolSpecs()[1], func(ctx context.Context, req *mcp.CallToolRequest, in LocatorInput) (*mcp.CallToolResult, ToolOutput[files.DirectoryListing], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[files.DirectoryListing](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[files.DirectoryListing](req, errors.New("member files service is unavailable"))
		}
		item, err := deps.MemberFiles.List(ctx, ps.Subject, in.locator())
		if err != nil {
			return toolFailure[files.DirectoryListing](req, err)
		}
		return nil, ToolOutput[files.DirectoryListing]{Data: item}, nil
	})
	addTool(server, OrdinaryToolSpecs()[2], func(ctx context.Context, req *mcp.CallToolRequest, in LocatorInput) (*mcp.CallToolResult, ToolOutput[files.Entry], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[files.Entry](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[files.Entry](req, errors.New("member files service is unavailable"))
		}
		item, err := deps.MemberFiles.Metadata(ctx, ps.Subject, in.locator())
		if err != nil {
			return toolFailure[files.Entry](req, err)
		}
		return nil, ToolOutput[files.Entry]{Data: item}, nil
	})
	addTool(server, OrdinaryToolSpecs()[3], func(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, ToolOutput[memberfiles.SearchResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.SearchResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.SearchResult](req, errors.New("member files service is unavailable"))
		}
		item, err := deps.MemberFiles.Search(ctx, ps.Subject, memberfiles.SearchRequest{Source: in.Source, MountID: in.MountID, Query: in.Query, Limit: in.Limit, Cursor: in.Cursor})
		if err != nil {
			return toolFailure[memberfiles.SearchResult](req, err)
		}
		return nil, ToolOutput[memberfiles.SearchResult]{Data: item}, nil
	})
	addTool(server, OrdinaryToolSpecs()[4], func(ctx context.Context, req *mcp.CallToolRequest, in TextInput) (*mcp.CallToolResult, ToolOutput[memberfiles.TextResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.TextResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.TextResult](req, errors.New("member files service is unavailable"))
		}
		max := in.MaxBytes
		if max == 0 {
			max = memberfiles.DefaultReadTextBytes
		}
		if max > memberfiles.MaxReadTextBytes {
			max = memberfiles.MaxReadTextBytes
		}
		item, err := deps.MemberFiles.ReadText(ctx, ps.Subject, in.locator(), max)
		if err != nil {
			return toolFailure[memberfiles.TextResult](req, err)
		}
		return nil, ToolOutput[memberfiles.TextResult]{Data: item}, nil
	})
	addTool(server, OrdinaryToolSpecs()[5], func(ctx context.Context, req *mcp.CallToolRequest, in LocatorInput) (*mcp.CallToolResult, ToolOutput[DownloadTicketOutput], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[DownloadTicketOutput](req, err)
		}
		if deps.TransferTickets == nil {
			return toolFailure[DownloadTicketOutput](req, errors.New("transfer ticket service is unavailable"))
		}
		intent, err := beginAudit(ctx, deps, req, "files.prepare_download", ps.Principal, in.locator())
		if err != nil {
			return toolFailure[DownloadTicketOutput](req, err)
		}
		issued, err := deps.TransferTickets.IssueDownload(ctx, ps.Principal, in.locator(), 0)
		if err != nil {
			finishAudit(ctx, deps, req, intent, "failed", ps.Principal, in.locator(), err)
			return toolFailure[DownloadTicketOutput](req, err)
		}
		out := DownloadTicketOutput{URL: issued.URL, Headers: map[string]string{"Authorization": "Bearer " + issued.BearerToken}, Size: issued.Size, ETag: issued.ObjectFingerprint, Fingerprint: issued.ObjectFingerprint, ExpiresAt: issued.ExpiresAt}
		if err := finishAudit(ctx, deps, req, intent, "succeeded", ps.Principal, in.locator(), nil); err != nil {
			return toolFailure[DownloadTicketOutput](req, err)
		}
		return nil, ToolOutput[DownloadTicketOutput]{Data: out}, nil
	})
	addTool(server, OrdinaryToolSpecs()[6], func(ctx context.Context, req *mcp.CallToolRequest, in DirectoryInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.MutationResult](req, errors.New("member files service is unavailable"))
		}
		return mutate(ctx, deps, req, "directories.create", ps.Principal, in.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.CreateDirectory(ctx, ps.Subject, in.locator(), in.Name)
		})
	})
	addTool(server, OrdinaryToolSpecs()[7], func(ctx context.Context, req *mcp.CallToolRequest, in UploadPrepareInput) (*mcp.CallToolResult, ToolOutput[UploadTicketOutput], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		if deps.MemberFiles == nil || deps.TransferTickets == nil {
			return toolFailure[UploadTicketOutput](req, errors.New("upload services are unavailable"))
		}
		intent, err := beginAudit(ctx, deps, req, "files.prepare_upload", ps.Principal, in.locator())
		if err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		upload, err := deps.MemberFiles.PrepareUpload(ctx, ps.Subject, memberfiles.UploadRequest{Locator: in.locator(), ExpectedSize: in.ExpectedSize, Checksum: in.Checksum})
		if err != nil {
			finishAudit(ctx, deps, req, intent, "failed", ps.Principal, in.locator(), err)
			return toolFailure[UploadTicketOutput](req, err)
		}
		issued, err := deps.TransferTickets.IssueUpload(ctx, ps.Principal, upload.ID, in.locator(), upload.ExpectedSize)
		if err != nil {
			_ = deps.MemberFiles.CancelUpload(ctx, ps.Subject, upload.ID)
			finishAudit(ctx, deps, req, intent, "failed", ps.Principal, in.locator(), err)
			return toolFailure[UploadTicketOutput](req, err)
		}
		out := UploadTicketOutput{ID: upload.ID, TargetPath: upload.TargetPath, ExpectedSize: upload.ExpectedSize, Checksum: upload.Checksum, PartSize: upload.PartSize, URLTemplate: issued.URL + "/parts/{partNumber}", Headers: map[string]string{"Authorization": "Bearer " + issued.BearerToken}, ExpiresAt: issued.ExpiresAt}
		if err := finishAudit(ctx, deps, req, intent, "succeeded", ps.Principal, in.locator(), nil); err != nil {
			return toolFailure[UploadTicketOutput](req, err)
		}
		return nil, ToolOutput[UploadTicketOutput]{Data: out}, nil
	})
	addTool(server, OrdinaryToolSpecs()[8], uploadStatusHandler(deps))
	addTool(server, OrdinaryToolSpecs()[9], uploadCompleteHandler(deps))
	addTool(server, OrdinaryToolSpecs()[10], uploadCancelHandler(deps))
	addTool(server, OrdinaryToolSpecs()[11], func(ctx context.Context, req *mcp.CallToolRequest, in RenameInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.MutationResult](req, errors.New("member files service is unavailable"))
		}
		return mutate(ctx, deps, req, "files.rename", ps.Principal, in.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.RenameSecure(ctx, ps.Subject, in.locator(), in.Destination, nil)
		})
	})
	addTool(server, OrdinaryToolSpecs()[12], func(ctx context.Context, req *mcp.CallToolRequest, in CopyInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.MutationResult](req, errors.New("member files service is unavailable"))
		}
		return mutate(ctx, deps, req, "files.copy", ps.Principal, in.Source.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.Copy(ctx, ps.Subject, in.Source.locator(), in.Destination.locator())
		})
	})
	addTool(server, OrdinaryToolSpecs()[13], func(ctx context.Context, req *mcp.CallToolRequest, in LocatorInput) (*mcp.CallToolResult, ToolOutput[memberfiles.TrashListResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.TrashListResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.TrashListResult](req, errors.New("member files service is unavailable"))
		}
		item, err := deps.MemberFiles.ListTrash(ctx, ps.Subject, in.locator())
		if err != nil {
			return toolFailure[memberfiles.TrashListResult](req, err)
		}
		return nil, ToolOutput[memberfiles.TrashListResult]{Data: item}, nil
	})
	addTool(server, OrdinaryToolSpecs()[14], func(ctx context.Context, req *mcp.CallToolRequest, in RestoreInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.MutationResult](req, errors.New("member files service is unavailable"))
		}
		return mutate(ctx, deps, req, "trash.restore", ps.Principal, in.locator(), func() (memberfiles.MutationResult, error) {
			return deps.MemberFiles.RestoreTrash(ctx, ps.Subject, in.locator(), in.TrashID)
		})
	})
	addTool(server, OrdinaryToolSpecs()[15], func(ctx context.Context, req *mcp.CallToolRequest, in ShareListInput) (*mcp.CallToolResult, ToolOutput[[]membershare.Share], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[[]membershare.Share](req, err)
		}
		if deps.MemberShares == nil {
			return toolFailure[[]membershare.Share](req, errors.New("member share service is unavailable"))
		}
		items, err := deps.MemberShares.List(ctx, ps.Subject, membershare.ListFilter{Limit: in.Limit})
		if err != nil {
			return toolFailure[[]membershare.Share](req, err)
		}
		return nil, ToolOutput[[]membershare.Share]{Data: items}, nil
	})
	server.AddReceivingMiddleware(scopeFilterMiddleware())
	server.AddReceivingMiddleware(rejectLegacyArgumentsMiddleware())
	server.AddReceivingMiddleware(cachePrivateMiddleware())
}

func addTool[In, Out any](server *mcp.Server, spec ToolSpec, handler mcp.ToolHandlerFor[In, ToolOutput[Out]]) {
	mcp.AddTool(server, toolFromSpec(spec), handler)
}

func uploadStatusHandler(deps ToolDependencies) mcp.ToolHandlerFor[UploadIDInput, ToolOutput[memberfiles.UploadStatusResult]] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in UploadIDInput) (*mcp.CallToolResult, ToolOutput[memberfiles.UploadStatusResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.UploadStatusResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.UploadStatusResult](req, errors.New("member files service is unavailable"))
		}
		item, err := deps.MemberFiles.UploadStatus(ctx, ps.Subject, in.UploadID)
		if err != nil {
			return toolFailure[memberfiles.UploadStatusResult](req, err)
		}
		return nil, ToolOutput[memberfiles.UploadStatusResult]{Data: item}, nil
	}
}

func uploadCompleteHandler(deps ToolDependencies) mcp.ToolHandlerFor[UploadIDInput, ToolOutput[memberfiles.MutationResult]] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in UploadIDInput) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[memberfiles.MutationResult](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[memberfiles.MutationResult](req, errors.New("member files service is unavailable"))
		}
		return mutate(ctx, deps, req, "uploads.complete", ps.Principal, access.Locator{Path: in.UploadID}, func() (memberfiles.MutationResult, error) {
			item, err := deps.MemberFiles.CompleteUpload(ctx, ps.Subject, in.UploadID)
			if err != nil {
				return item, err
			}
			if deps.TransferTickets != nil {
				if err := deps.TransferTickets.CloseUploadTickets(ctx, in.UploadID, transferticket.StatusCompleted); err != nil {
					return memberfiles.MutationResult{}, err
				}
			}
			return item, nil
		})
	}
}

func uploadCancelHandler(deps ToolDependencies) mcp.ToolHandlerFor[UploadIDInput, ToolOutput[UploadCancelOutput]] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in UploadIDInput) (*mcp.CallToolResult, ToolOutput[UploadCancelOutput], error) {
		ps, err := deps.subject(req)
		if err != nil {
			return toolFailure[UploadCancelOutput](req, err)
		}
		if deps.MemberFiles == nil {
			return toolFailure[UploadCancelOutput](req, errors.New("member files service is unavailable"))
		}
		intent, err := beginAudit(ctx, deps, req, "uploads.cancel", ps.Principal, access.Locator{Path: in.UploadID})
		if err != nil {
			return toolFailure[UploadCancelOutput](req, err)
		}
		err = deps.MemberFiles.CancelUpload(ctx, ps.Subject, in.UploadID)
		if err != nil {
			finishAudit(ctx, deps, req, intent, "failed", ps.Principal, access.Locator{Path: in.UploadID}, err)
			return toolFailure[UploadCancelOutput](req, err)
		}
		if deps.TransferTickets != nil {
			if err := deps.TransferTickets.CloseUploadTickets(ctx, in.UploadID, transferticket.StatusCanceled); err != nil {
				finishAudit(ctx, deps, req, intent, "failed", ps.Principal, access.Locator{Path: in.UploadID}, err)
				return toolFailure[UploadCancelOutput](req, err)
			}
		}
		if err := finishAudit(ctx, deps, req, intent, "succeeded", ps.Principal, access.Locator{Path: in.UploadID}, nil); err != nil {
			return toolFailure[UploadCancelOutput](req, err)
		}
		return nil, ToolOutput[UploadCancelOutput]{Data: UploadCancelOutput{UploadID: in.UploadID, Canceled: true}}, nil
	}
}

func mutate(ctx context.Context, deps ToolDependencies, req *mcp.CallToolRequest, name string, principal aitoken.Principal, locator access.Locator, fn func() (memberfiles.MutationResult, error)) (*mcp.CallToolResult, ToolOutput[memberfiles.MutationResult], error) {
	intent, err := beginAudit(ctx, deps, req, name, principal, locator)
	if err != nil {
		return toolFailure[memberfiles.MutationResult](req, err)
	}
	item, err := fn()
	if err != nil {
		finishAudit(ctx, deps, req, intent, "failed", principal, locator, err)
		return toolFailure[memberfiles.MutationResult](req, err)
	}
	if err := finishAudit(ctx, deps, req, intent, "succeeded", principal, locator, nil); err != nil {
		return toolFailure[memberfiles.MutationResult](req, err)
	}
	return nil, ToolOutput[memberfiles.MutationResult]{Data: item}, nil
}

func toolFailure[T any](req *mcp.CallToolRequest, err error) (*mcp.CallToolResult, ToolOutput[T], error) {
	if err == nil {
		err = errors.New("unknown MCP tool error")
	}
	requestID := ""
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		requestID = headerValue(req.Extra.Header, "X-Request-ID")
	}
	code, retryable := stableErrorCode(err)
	if code == "internal" {
		return nil, ToolOutput[T]{}, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal MCP error"}
	}
	return &mcp.CallToolResult{IsError: true}, ToolOutput[T]{Error: &ToolError{Code: code, Message: safeErrorMessage(code), RequestID: requestID, Retryable: retryable}}, nil
}

func safeErrorMessage(code string) string {
	switch code {
	case "forbidden":
		return "The requested resource is not accessible."
	case "boundary_violation":
		return "The target is outside the token boundary."
	case "readonly_mount":
		return "The target mount is read-only."
	case "mount_identity_unverifiable":
		return "The target mount identity could not be verified."
	case "not_found":
		return "The requested resource was not found."
	case "conflict":
		return "The operation conflicts with the current resource state."
	case "ticket_expired":
		return "The transfer ticket or upload session has expired."
	case "upload_conflict":
		return "The upload session conflicts with the current resource state."
	case "cross_mount_incomplete":
		return "The move reached a partial state; the published destination is preserved for recovery."
	case "invalid_input":
		return "The request contains invalid input."
	case "client_capability_required":
		return "This operation requires a client with reliable form confirmation support."
	case "confirmation_declined":
		return "The confirmation was declined; no changes were made."
	case "confirmation_stale":
		return "The confirmation no longer matches the current request; no changes were made."
	case "confirmation_expired":
		return "The confirmation has expired; no changes were made."
	case "confirmation_replayed":
		return "The confirmation was already consumed; no changes were made."
	case "confirmation_invalid":
		return "The confirmation response was invalid; no changes were made."
	case "confirmation_unavailable":
		return "Confirmation service is temporarily unavailable; no changes were made."
	default:
		return "The MCP operation failed."
	}
}

func stableErrorCode(err error) (string, bool) {
	switch {
	case errors.Is(err, access.ErrBoundaryViolation):
		return "boundary_violation", false
	case errors.Is(err, access.ErrReadonlyMount):
		return "readonly_mount", false
	case errors.Is(err, access.ErrMountIdentityUnverifiable):
		return "mount_identity_unverifiable", true
	case errors.Is(err, access.ErrForbidden), errors.Is(err, access.ErrUnauthorized):
		return "forbidden", false
	case errors.Is(err, membershare.ErrForbidden), errors.Is(err, membershare.ErrUnauthorized):
		return "forbidden", false
	case errors.Is(err, transferticket.ErrTicketExpired):
		return "ticket_expired", false
	case errors.Is(err, transferticket.ErrTicketNotFound):
		return "not_found", false
	case errors.Is(err, transferticket.ErrTicketClosed):
		return "conflict", true
	case errors.Is(err, transferticket.ErrTicketInvalid), errors.Is(err, transferticket.ErrInvalidInput):
		return "invalid_input", false
	case errors.Is(err, transferticket.ErrUploadInvalid):
		return "upload_conflict", true
	case errors.Is(err, ErrClientCapability):
		return "client_capability_required", false
	case errors.Is(err, ErrConfirmationDeclined):
		return "confirmation_declined", false
	case errors.Is(err, ErrConfirmationStale):
		return "confirmation_stale", false
	case errors.Is(err, ErrConfirmationInvalid), errors.Is(err, confirmation.ErrInvalidInput):
		return "confirmation_invalid", false
	case errors.Is(err, confirmation.ErrExpired):
		return "confirmation_expired", false
	case errors.Is(err, confirmation.ErrConsumed):
		return "confirmation_replayed", false
	case errors.Is(err, confirmation.ErrUnavailable):
		return "confirmation_unavailable", true
	case errors.Is(err, memberfiles.ErrUploadConflict):
		return "upload_conflict", true
	case errors.Is(err, memberfiles.ErrMutationInvalidPath), errors.Is(err, memberfiles.ErrCrossMountSameMount), errors.Is(err, files.ErrNotManagedMount):
		return "invalid_input", false
	case errors.Is(err, memberfiles.ErrMutationConflict):
		return "conflict", true
	case errors.Is(err, memberfiles.ErrUploadExpired):
		return "ticket_expired", false
	case errors.Is(err, files.ErrCrossMountIncomplete):
		return "cross_mount_incomplete", true
	case errors.Is(err, membershare.ErrNotFound), errors.Is(err, memberfiles.ErrUploadNotFound):
		return "not_found", false
	case errors.Is(err, os.ErrNotExist):
		return "not_found", false
	case errors.Is(err, memberfiles.ErrInvalidInput), errors.Is(err, membershare.ErrInvalidInput), errors.Is(err, access.ErrInvalidRequest):
		return "invalid_input", false
	case strings.Contains(strings.ToLower(err.Error()), "not found"):
		return "not_found", false
	case strings.Contains(strings.ToLower(err.Error()), "conflict"):
		return "conflict", true
	default:
		return "internal", true
	}
}

func beginAudit(ctx context.Context, deps ToolDependencies, req *mcp.CallToolRequest, tool string, principal aitoken.Principal, locator access.Locator) (int64, error) {
	if !deps.AuditEnabled {
		return 0, nil
	}
	event := auditEvent(ctx, req, tool, "intent", principal, locator)
	return deps.AuditRecorder.RecordMCPIntent(ctx, event)
}

func finishAudit(ctx context.Context, deps ToolDependencies, req *mcp.CallToolRequest, intent int64, result string, principal aitoken.Principal, locator access.Locator, cause error) error {
	if !deps.AuditEnabled || intent == 0 {
		return nil
	}
	tool := "mcp_tool"
	if req != nil && req.Params != nil && req.Params.Name != "" {
		tool = req.Params.Name
	}
	event := auditEvent(ctx, req, tool, result, principal, locator)
	if cause != nil {
		event.MetadataJSON = `{"errorCode":"` + stableCodeOnly(cause) + `"}`
	}
	if err := deps.AuditRecorder.RecordMCPOutcome(ctx, intent, event); err != nil {
		_ = deps.AuditRecorder.MarkMCPReadinessRisk(ctx, err)
		return err
	}
	return nil
}

func stableCodeOnly(err error) string { code, _ := stableErrorCode(err); return code }

func auditEvent(ctx context.Context, req *mcp.CallToolRequest, tool, result string, principal aitoken.Principal, locator access.Locator) audit.MCPEvent {
	locator = auditLocator(locator)
	requestID := ""
	traceID := ""
	if ctx != nil {
		requestID = httpx.RequestID(ctx)
	}
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		if requestID == "" {
			requestID = headerValue(req.Extra.Header, "X-Request-ID")
		}
		traceID = headerValue(req.Extra.Header, "X-Trace-ID")
	}
	metadata := map[string]any{"source": locator.Source, "path": locator.Path}
	targetID := string(locator.Source) + ":" + locator.Path
	if locator.MountID != "" {
		metadata["mountId"] = locator.MountID
		targetID = string(locator.Source) + ":" + locator.MountID + ":" + locator.Path
	}
	metadataJSON, _ := json.Marshal(metadata)
	return audit.MCPEvent{AccountID: principal.AccountID, CredentialPublicID: principal.PublicID, ToolName: tool, Result: result, RequestID: requestID, TraceID: traceID, TargetType: "member_file", TargetID: targetID, MetadataJSON: string(metadataJSON)}
}

func headerValue(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func auditLocator(locator access.Locator) access.Locator {
	cleaned, err := storage.CleanRelativePath(locator.Path)
	if err != nil {
		locator.Path = "<invalid>"
	} else {
		locator.Path = cleaned
	}
	return locator
}

func rejectLegacyArgumentsMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == methodToolsCall {
				call, ok := req.(*mcp.CallToolRequest)
				if ok && call.Params != nil {
					raw, err := json.Marshal(call.Params.Arguments)
					if err != nil || containsForbiddenMCPField(raw) {
						return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid MCP locator"}
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}

func containsForbiddenMCPField(raw []byte) bool {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return len(raw) != 0
	}
	forbidden := map[string]bool{
		"spaceId": true, "space_id": true, "accountId": true,
		"defaultMountId": true, "collaborationId": true,
	}
	var walk func(any) bool
	walk = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if forbidden[key] || walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

func scopeFilterMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != methodToolsList {
				return result, err
			}
			list, ok := result.(*mcp.ListToolsResult)
			if !ok {
				return result, err
			}
			allowed := map[string]bool{}
			if req.GetExtra() != nil && req.GetExtra().TokenInfo != nil {
				for _, scope := range req.GetExtra().TokenInfo.Scopes {
					allowed[scope] = true
				}
			}
			highRiskAllowed := false
			if listReq, ok := req.(*mcp.ListToolsRequest); ok {
				highRiskAllowed = reliableFormCapabilities(listReq.ClientCapabilities())
			}
			filtered := list.Tools[:0]
			for _, tool := range list.Tools {
				for _, spec := range append(OrdinaryToolSpecs(), HighRiskToolSpecs()...) {
					if _, highRisk := highRiskSpec(spec.Name); highRisk && !highRiskAllowed {
						continue
					}
					if spec.Name == tool.Name && allowed[string(spec.Scope)] {
						filtered = append(filtered, tool)
						break
					}
				}
			}
			list.Tools = filtered
			list.NextCursor = ""
			return list, nil
		}
	}
}

func highRiskSpec(name string) (ToolSpec, bool) {
	for _, spec := range HighRiskToolSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return ToolSpec{}, false
}

func cachePrivateMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err == nil && method == methodToolsList {
				if list, ok := result.(*mcp.ListToolsResult); ok {
					list.TTLMs = 0
					list.CacheScope = "private"
				}
			}
			return result, err
		}
	}
}
