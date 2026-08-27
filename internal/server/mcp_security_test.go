package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/config"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/store"
)

func TestMCPRouteDisabledBeforeProtocolHandling(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "route_group_disabled" {
		t.Fatalf("error code = %q, want route_group_disabled", body.Error.Code)
	}
}

func TestMCPNetworkSecurityAndProtocolLifecycle(t *testing.T) {
	db, handler, bearer := newMCPHTTPTestServer(t, config.MCPConfig{
		AllowedHosts:   []string{"mcp.example.test"},
		AllowedOrigins: []string{"https://mcp.example.test"},
		MaxBodyBytes:   1 << 20,
	})
	defer db.Close()

	request := func(method, host, origin string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+host+"/mcp", bytes.NewReader(body))
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if got := request(http.MethodPost, "other.example.test", "", []byte(`{}`)).Code; got != http.StatusForbidden {
		t.Fatalf("disallowed host status = %d, want 403", got)
	}
	if got := request(http.MethodPost, "mcp.example.test", "https://other.example.test", []byte(`{}`)).Code; got != http.StatusForbidden {
		t.Fatalf("disallowed origin status = %d, want 403", got)
	}
	if got := request(http.MethodPost, "mcp.example.test", "", []byte(`{}`)).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400", got)
	}
	if got := request(http.MethodPost, "mcp.example.test", "", []byte(`{"method":"tools/list","params":{}}`)).Code; got == http.StatusOK {
		t.Fatal("legacy {method,params} envelope unexpectedly succeeded")
	}

	discover := []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"inspector","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	discoverReq := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", bytes.NewReader(discover))
	discoverReq.Host = "mcp.example.test"
	discoverReq.Header.Set("Authorization", "Bearer "+bearer)
	discoverReq.Header.Set("Content-Type", "application/json")
	discoverReq.Header.Set("Accept", "application/json, text/event-stream")
	discoverReq.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	discoverReq.Header.Set("Mcp-Method", "server/discover")
	discoverRec := httptest.NewRecorder()
	handler.ServeHTTP(discoverRec, discoverReq)
	if discoverRec.Code != http.StatusOK {
		t.Fatalf("discover status = %d, body = %s", discoverRec.Code, discoverRec.Body.String())
	}

	initialize := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"inspector","version":"1"}}}`)
	initializeRec := request(http.MethodPost, "mcp.example.test", "", initialize)
	rec := initializeRec
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode initialize: %v; body=%s", err, rec.Body.String())
	}
	if result.Result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("protocolVersion = %q, want 2025-11-25", result.Result.ProtocolVersion)
	}

	if got := request(http.MethodGet, "mcp.example.test", "", nil).Code; got != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", got)
	}
	if got := request(http.MethodPost, "mcp.example.test", "", bytes.Repeat([]byte("x"), (1<<20)+1)).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", got)
	}
}

func TestMCPRejectsMissingInvalidRevokedAndExpiredTokens(t *testing.T) {
	db, handler, bearer := newMCPHTTPTestServer(t, config.MCPConfig{AllowedHosts: []string{"mcp.example.test"}})
	defer db.Close()

	request := func(authorization string) int {
		req := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", strings.NewReader(`{}`))
		req.Host = "mcp.example.test"
		req.Header.Set("Content-Type", "application/json")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := request(""); got != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", got)
	}
	if got := request("Bearer invalid"); got != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", got)
	}
	if _, err := db.SQL().Exec(`UPDATE ai_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE public_id = ?`, strings.SplitN(bearer, ".", 2)[0]); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if got := request("Bearer " + bearer); got != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", got)
	}
	if _, err := db.SQL().Exec(`UPDATE ai_tokens SET revoked_at = NULL, expires_at = ? WHERE public_id = ?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), strings.SplitN(bearer, ".", 2)[0]); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	if got := request("Bearer " + bearer); got != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want 401", got)
	}
}

func TestMCPRejectsMissingAllowedHostConfiguration(t *testing.T) {
	handler := New(config.Config{
		Routes: map[domain.RouteGroup]bool{domain.RouteGroupMCP: true},
		MCP:    config.MCPConfig{MaxBodyBytes: 1 << 20},
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", strings.NewReader(`{}`))
	req.Host = "mcp.example.test"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "allowedhosts") {
		t.Fatalf("response leaked configuration internals: %s", rec.Body.String())
	}
}

func newMCPHTTPTestServer(t *testing.T, mcpConfig config.MCPConfig) (*store.DB, http.Handler, string) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "mcp.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery ready: %v", err)
	}
	admin, _ := createAPITestAccounts(t, db)
	createTestMount(t, db, "mcp-mount", admin.ID, "read_write")
	issued, err := aitoken.NewService(db.SQL()).Create(context.Background(), aitoken.CreateRequest{
		AccountID: admin.ID,
		Name:      "mcp-test",
		Scopes:    []aitoken.Scope{aitoken.ScopeMountsRead},
		Boundaries: []aitoken.DirectoryBoundary{{
			Source: contentref.SourceCommonMount, MountID: "mcp-mount", RelativePath: ".",
		}},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	handler := New(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupMCP: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupMCP: true},
		MCP:               mcpConfig,
	}, db)
	return db, handler, issued.BearerToken
}
