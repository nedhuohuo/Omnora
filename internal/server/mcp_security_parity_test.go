package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPAccountDisableRejectsTheNextRequest(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	call := mcpToolsCallBody("files.list", map[string]any{
		"source":  "common_mount",
		"mountId": "mcp-protocol-mount",
		"path":    ".",
	})
	if response := mcpRawRequest(fixture, http.MethodPost, "/mcp", call, "", "", nil); response.Code != http.StatusOK {
		t.Fatalf("MCP request before account disable = %d, want 200", response.Code)
	}
	if _, err := fixture.db.SQL().Exec(`UPDATE accounts SET status = 'disabled' WHERE id = ?`, fixture.accountID); err != nil {
		t.Fatalf("disable MCP fixture account: %v", err)
	}
	response := mcpRawRequest(fixture, http.MethodPost, "/mcp", call, "", "", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("MCP request after account disable = %d, want 401", response.Code)
	}
}

func TestMCPReadOnlyMountChangeRejectsWrite(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	if _, err := fixture.db.SQL().Exec(`UPDATE mounts SET mode = 'read_only' WHERE id = 'mcp-protocol-mount'`); err != nil {
		t.Fatalf("make MCP fixture mount read-only: %v", err)
	}
	call := mcpToolsCallBody("directories.create", map[string]any{
		"source":  "common_mount",
		"mountId": "mcp-protocol-mount",
		"path":    ".",
		"name":    "must-not-exist",
	})
	response := mcpRawRequest(fixture, http.MethodPost, "/mcp", call, "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "readonly_mount") {
		t.Fatalf("read-only MCP write status/body = %d/%s, want typed readonly_mount error", response.Code, response.Body.String())
	}
	if _, err := os.Stat(fixtureMountPath(fixture, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("read-only MCP write created an object, stat err = %v", err)
	}
}

func TestMCPMountIdentityDriftRejectsTheNextRequest(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	if _, err := fixture.db.SQL().Exec(`UPDATE mounts SET mount_identity_json = '{}' WHERE id = 'mcp-protocol-mount'`); err != nil {
		t.Fatalf("drift MCP fixture mount identity: %v", err)
	}
	call := mcpToolsCallBody("files.list", map[string]any{
		"source":  "common_mount",
		"mountId": "mcp-protocol-mount",
		"path":    ".",
	})
	response := mcpRawRequest(fixture, http.MethodPost, "/mcp", call, "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "mount_identity_unverifiable") {
		t.Fatalf("identity drift MCP status/body = %d/%s, want typed mount identity error", response.Code, response.Body.String())
	}
}

// TestRESTAndMCPShareAuthorizationParity uses the same account, ACL, mount,
// and application service graph for representative read, write, and share
// operations. It compares observable success/error outcomes without logging
// file content, token material, or one-time share fragments.
func TestRESTAndMCPRepresentativeAuthorizationParity(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	cookie := issueAPITestSession(t, fixture.db, fixture.accountID)

	restRead := httptest.NewRequest(http.MethodGet, "/api/v1/member/files/children?source=common_mount&mountId=mcp-protocol-mount&path=.", nil)
	restRead.AddCookie(cookie)
	restReadRecorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(restReadRecorder, restRead)
	if restReadRecorder.Code != http.StatusOK || !strings.Contains(restReadRecorder.Body.String(), "hello.txt") {
		t.Fatalf("REST representative read status/body = %d/%s", restReadRecorder.Code, restReadRecorder.Body.String())
	}
	mcpRead := mcpRawRequest(fixture, http.MethodPost, "/mcp", mcpToolsCallBody("files.list", map[string]any{
		"source":  "common_mount",
		"mountId": "mcp-protocol-mount",
		"path":    ".",
	}), "", "", nil)
	if mcpRead.Code != http.StatusOK || strings.Contains(mcpRead.Body.String(), `"isError":true`) || !strings.Contains(mcpRead.Body.String(), "hello.txt") {
		t.Fatalf("MCP representative read status/body = %d/%s", mcpRead.Code, mcpRead.Body.String())
	}

	mcpWrite := mcpRawRequest(fixture, http.MethodPost, "/mcp", mcpToolsCallBody("directories.create", map[string]any{
		"source":  "common_mount",
		"mountId": "mcp-protocol-mount",
		"path":    ".",
		"name":    "mcp-parity-dir",
	}), "", "", nil)
	if mcpWrite.Code != http.StatusOK || strings.Contains(mcpWrite.Body.String(), `"isError":true`) {
		t.Fatalf("MCP representative write status/body = %d/%s", mcpWrite.Code, mcpWrite.Body.String())
	}

	client, closeClient := fixture.client(t, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}},
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmed": true}}, nil
		},
	})
	defer closeClient()
	session, err := client.Connect(context.Background(), fixture.transport(), nil)
	if err != nil {
		t.Fatalf("connect MCP parity client: %v", err)
	}
	defer session.Close()
	mcpShare, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "shares.create",
		Arguments: map[string]any{
			"source":        "common_mount",
			"mountId":       "mcp-protocol-mount",
			"path":          "hello.txt",
			"allowPreview":  true,
			"allowDownload": true,
		},
	})
	if err != nil {
		t.Fatalf("MCP representative share call: %v", err)
	}
	if mcpShare == nil || mcpShare.IsError {
		t.Fatal("MCP representative share returned an MCP error")
	}
	var shareCount int
	if err := fixture.db.SQL().QueryRow(`SELECT COUNT(1) FROM shares WHERE mount_id = 'mcp-protocol-mount'`).Scan(&shareCount); err != nil {
		t.Fatalf("count parity shares: %v", err)
	}
	if shareCount != 1 {
		t.Fatalf("MCP representative share count = %d, want one successful creation", shareCount)
	}
}

func mcpToolsCallBody(name string, arguments map[string]any) []byte {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": arguments},
	}
	body, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	return body
}

func fixtureMountPath(fixture *mcpProtocolFixture, relative string) string {
	var root string
	if err := fixture.db.SQL().QueryRow(`SELECT root_path FROM mounts WHERE id = 'mcp-protocol-mount'`).Scan(&root); err != nil {
		return relative
	}
	return filepath.Join(root, relative)
}
