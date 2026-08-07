package mcpapi

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/files"
	"omnora/internal/httpx"
	"omnora/internal/memberfiles"
	"omnora/internal/transferticket"
)

func TestOrdinaryToolCatalogIsStable(t *testing.T) {
	want := []string{"spaces.list", "mounts.list", "files.list", "files.metadata", "files.search", "files.read_text", "files.prepare_download", "directories.create", "files.prepare_upload", "uploads.status", "uploads.complete", "uploads.cancel", "files.rename", "files.copy", "trash.list", "trash.restore", "shares.list"}
	specs := OrdinaryToolSpecs()
	if len(specs) != len(want) {
		t.Fatalf("ordinary tool count = %d, want %d", len(specs), len(want))
	}
	for i, name := range want {
		if specs[i].Name != name {
			t.Fatalf("tool[%d] = %q, want %q", i, specs[i].Name, name)
		}
		if specs[i].Scope == "" {
			t.Errorf("tool %s has no scope", name)
		}
	}
}

func TestRegisterOrdinaryToolsBuildsSchemas(t *testing.T) {
	server := NewServer(nil)
	RegisterOrdinaryTools(server, ToolDependencies{})
	// AddTool panics on invalid typed schemas. A tools/list request is not
	// needed here; registration itself is the schema validation boundary.
	if server == nil {
		t.Fatal("server is nil")
	}
}

func TestToolsListFiltersCurrentScopesAndIsPrivate(t *testing.T) {
	allTools := make([]*mcp.Tool, 0, len(OrdinaryToolSpecs()))
	for _, spec := range OrdinaryToolSpecs() {
		allTools = append(allTools, toolFromSpec(spec))
	}
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		copyTools := append([]*mcp.Tool(nil), allTools...)
		return &mcp.ListToolsResult{Tools: copyTools}, nil
	}
	middleware := scopeFilterMiddleware()(cachePrivateMiddleware()(next))
	fullScopes := make([]string, 0, len(OrdinaryToolSpecs()))
	for _, spec := range OrdinaryToolSpecs() {
		fullScopes = append(fullScopes, string(spec.Scope))
	}
	full, err := middleware(context.Background(), methodToolsList, &mcp.ServerRequest[*mcp.ListToolsParams]{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Scopes: fullScopes}}})
	if err != nil {
		t.Fatalf("full tools/list error = %v", err)
	}
	fullResult := full.(*mcp.ListToolsResult)
	if len(fullResult.Tools) != 17 || fullResult.TTLMs != 0 || fullResult.CacheScope != "private" || fullResult.NextCursor != "" {
		t.Fatalf("full tools/list = count %d ttl %d scope %q cursor %q", len(fullResult.Tools), fullResult.TTLMs, fullResult.CacheScope, fullResult.NextCursor)
	}
	narrow, err := middleware(context.Background(), methodToolsList, &mcp.ServerRequest[*mcp.ListToolsParams]{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Scopes: []string{"files:list"}}}})
	if err != nil {
		t.Fatalf("narrow tools/list error = %v", err)
	}
	narrowResult := narrow.(*mcp.ListToolsResult)
	if len(narrowResult.Tools) != 1 || narrowResult.Tools[0].Name != "files.list" {
		t.Fatalf("narrow tools/list = %#v", narrowResult.Tools)
	}
}

func TestTypedSchemasUseLowerCamelFields(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(LocatorInput{}), reflect.TypeOf(SearchInput{}), reflect.TypeOf(UploadPrepareInput{}), reflect.TypeOf(UploadIDInput{}), reflect.TypeOf(DownloadTicketOutput{}), reflect.TypeOf(UploadTicketOutput{})} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" || name != strings.ToLower(name[:1])+name[1:] {
				t.Errorf("%s.%s has non-lowerCamel JSON name %q", typ.Name(), field.Name, name)
			}
		}
	}
}

func TestPrepareDownloadIsNotIdempotent(t *testing.T) {
	for _, spec := range OrdinaryToolSpecs() {
		if spec.Name != "files.prepare_download" {
			continue
		}
		if annotation := toolFromSpec(spec).Annotations; annotation == nil || annotation.IdempotentHint {
			t.Fatal("files.prepare_download must advertise IdempotentHint=false")
		}
		return
	}
	t.Fatal("files.prepare_download missing from catalogue")
}

func TestAuditEventUsesRequestTraceIDsAndSanitizesPath(t *testing.T) {
	req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: map[string][]string{"X-Trace-Id": []string{"trace-1"}}}}
	ctx := httpx.WithRequestID(context.Background(), "request-1")
	event := auditEvent(ctx, req, "files.rename", "intent", aitoken.Principal{AccountID: "acct", PublicID: "tok"}, access.Locator{SpaceID: "space", MountID: "mount", Path: "../secret"})
	if event.RequestID != "request-1" || event.TraceID != "trace-1" {
		t.Fatalf("audit ids = request %q trace %q", event.RequestID, event.TraceID)
	}
	if event.TargetID != "space/mount/<invalid>" || strings.Contains(event.MetadataJSON, "../secret") {
		t.Fatalf("audit target/path leaked: target=%q metadata=%q", event.TargetID, event.MetadataJSON)
	}
}

func TestStableErrorCodesCoverTransferFailures(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{transferticket.ErrTicketNotFound, "not_found"},
		{transferticket.ErrTicketClosed, "conflict"},
		{transferticket.ErrTicketInvalid, "invalid_input"},
		{transferticket.ErrInvalidInput, "invalid_input"},
		{transferticket.ErrUploadInvalid, "upload_conflict"},
		{memberfiles.ErrInvalidInput, "invalid_input"},
		{memberfiles.ErrMutationInvalidPath, "invalid_input"},
		{memberfiles.ErrCrossMountSameMount, "invalid_input"},
		{files.ErrNotManagedMount, "invalid_input"},
		{ErrConfirmationStale, "confirmation_stale"},
		{os.ErrNotExist, "not_found"},
	}
	for _, test := range tests {
		if code, _ := stableErrorCode(test.err); code != test.code {
			t.Errorf("stableErrorCode(%v) = %q, want %q", test.err, code, test.code)
		}
	}
	_, _, err := toolFailure[struct{}](nil, errors.New("database connection failed"))
	if wireErr, ok := err.(*jsonrpc.Error); !ok || wireErr.Code != jsonrpc.CodeInternalError {
		t.Fatalf("internal tool error = %#v, want JSON-RPC internal error", err)
	}
}
