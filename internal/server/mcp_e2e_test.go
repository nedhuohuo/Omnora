package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"omnora/internal/aitoken"
	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/store"
)

// TestMCPStreamableClientNegotiatesModernProtocolAndTypedRead exercises the
// public transport through the official Go SDK. The RoundTripper is the only
// place where test credentials are added; it deliberately never logs them.
func TestMCPStreamableClientNegotiatesModernProtocolAndTypedRead(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	client, closeClient := fixture.client(t, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{
			Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}},
		},
	})
	defer closeClient()

	ctx := context.Background()
	session, err := client.Connect(ctx, fixture.transport(), nil)
	if err != nil {
		t.Fatalf("official SDK connect: %v", err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("negotiated protocol = %q, want 2026-07-28", got)
	}
	if session.InitializeResult().ServerInfo == nil {
		t.Fatal("initialize result did not include server info")
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 24 {
		t.Fatalf("full-scope tools/list count = %d, want 24", len(tools.Tools))
	}
	if !containsMCPTool(tools.Tools, "shares.create") || !containsMCPTool(tools.Tools, "files.read_text") {
		t.Fatalf("tools/list did not include ordinary and form-gated tools")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "files.read_text",
		Arguments: map[string]any{
			"spaceId": "mcp-protocol-space",
			"mountId": "mcp-protocol-mount",
			"path":    "hello.txt",
		},
	})
	if err != nil {
		t.Fatalf("typed files.read_text call: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatal("typed files.read_text call returned an MCP error")
	}
	var output struct {
		Data struct {
			Text      string `json:"text"`
			BytesRead int64  `json:"bytesRead"`
		} `json:"data"`
	}
	if err := decodeMCPStructured(result.StructuredContent, &output); err != nil {
		t.Fatalf("decode typed read output: %v", err)
	}
	if output.Data.Text != "hello from MCP\n" || output.Data.BytesRead != int64(len("hello from MCP\n")) {
		t.Fatal("typed read output did not match the expected bounded text fields")
	}
}

// TestMCPStreamableClientAutomaticallyRetriesHighRiskElicitation verifies the
// SDK's MRTR middleware: the first call returns input_required, the handler
// accepts the form, and the original call is retried with opaque state.
func TestMCPStreamableClientAutomaticallyRetriesHighRiskElicitation(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	called := 0
	client, closeClient := fixture.client(t, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{
			Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}},
		},
		ElicitationHandler: func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			called++
			if request == nil || request.Params == nil || request.Params.Message == "" {
				t.Fatal("elicitation request did not include a confirmation message")
			}
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true}}, nil
		},
	})
	defer closeClient()
	session, err := client.Connect(context.Background(), fixture.transport(), nil)
	if err != nil {
		t.Fatalf("official SDK connect: %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "shares.create",
		Arguments: map[string]any{
			"spaceId":       "mcp-protocol-space",
			"mountId":       "mcp-protocol-mount",
			"path":          "hello.txt",
			"allowPreview":  true,
			"allowDownload": true,
		},
	})
	if err != nil {
		t.Fatalf("MRTR shares.create: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatal("MRTR shares.create returned an MCP error")
	}
	if called != 1 {
		t.Fatalf("elicitation handler calls = %d, want one", called)
	}
	var shareCount int
	if err := fixture.db.SQL().QueryRow(`SELECT COUNT(1) FROM shares WHERE space_id = 'mcp-protocol-space'`).Scan(&shareCount); err != nil {
		t.Fatalf("count created shares: %v", err)
	}
	if shareCount != 1 {
		t.Fatalf("created share count = %d, want one", shareCount)
	}
}

func containsMCPTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name == name {
			return true
		}
	}
	return false
}

func decodeMCPStructured(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

type mcpProtocolFixture struct {
	db        *store.DB
	server    *Server
	handler   http.Handler
	bearer    string
	accountID string
}

func newMCPProtocolFixture(t *testing.T) *mcpProtocolFixture {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "mcp-e2e.db")})
	if err != nil {
		t.Fatalf("open MCP fixture database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery ready: %v", err)
	}
	admin, _ := createAPITestAccounts(t, db)
	root := createTestMount(t, db, "mcp-protocol-space", "mcp-protocol-mount", admin.ID, "read_write", "external")
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from MCP\n"), 0o600); err != nil {
		t.Fatalf("write MCP fixture file: %v", err)
	}
	issued, err := aitoken.NewService(db.SQL()).Create(context.Background(), aitoken.CreateRequest{
		AccountID: admin.ID,
		Name:      "mcp-protocol-test",
		Scopes:    aitoken.AllowlistedScopes(),
		Boundaries: []aitoken.DirectoryBoundary{{
			SpaceID: "mcp-protocol-space", MountID: "mcp-protocol-mount", RelativePath: ".",
		}},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("issue MCP fixture token: %v", err)
	}
	srv := NewServer(config.Config{
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST:  true,
			domain.RouteGroupShare: true,
			domain.RouteGroupMCP:   true,
		},
		RouteEnvOverrides: map[domain.RouteGroup]bool{
			domain.RouteGroupREST:  true,
			domain.RouteGroupShare: true,
			domain.RouteGroupMCP:   true,
		},
		MCP: config.MCPConfig{AllowedHosts: []string{"mcp.example.test"}, MaxBodyBytes: 1 << 20},
	}, db)
	return &mcpProtocolFixture{db: db, server: srv, handler: srv.Handler(), bearer: issued.BearerToken, accountID: issued.Token.AccountID}
}

func (f *mcpProtocolFixture) transport() *mcp.StreamableClientTransport {
	return &mcp.StreamableClientTransport{Endpoint: "http://mcp.example.test/mcp", HTTPClient: &http.Client{
		Transport: mcpAuthRoundTripper{handler: f.handler, host: "mcp.example.test", bearer: f.bearer},
	}, MaxRetries: -1, DisableStandaloneSSE: true}
}

func (f *mcpProtocolFixture) client(t *testing.T, options *mcp.ClientOptions) (*mcp.Client, func()) {
	t.Helper()
	return mcp.NewClient(&mcp.Implementation{Name: "omnora-protocol-test", Version: "1"}, options), func() {}
}

type mcpAuthRoundTripper struct {
	base    http.RoundTripper
	handler http.Handler
	host    string
	bearer  string
}

func (rt mcpAuthRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := request.Clone(request.Context())
	clone.Host = rt.host
	clone.Header.Set("Authorization", "Bearer "+rt.bearer)
	if rt.handler != nil {
		recorder := httptest.NewRecorder()
		rt.handler.ServeHTTP(recorder, clone)
		response := recorder.Result()
		response.Request = clone
		if response.Body == nil {
			response.Body = io.NopCloser(http.NoBody)
		}
		return response, nil
	}
	return base.RoundTrip(clone)
}
