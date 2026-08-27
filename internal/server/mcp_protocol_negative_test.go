package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPRawProtocolLifecycleAndFormCatalogue(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	initialize := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"protocol-test","version":"1"}}}`)
	response := mcpRawRequest(fixture, http.MethodPost, "/mcp", initialize, "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("legacy initialize status = %d body=%s", response.Code, response.Body.String())
	}
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &initialized); err != nil {
		t.Fatalf("decode legacy initialize: %v", err)
	}
	if initialized.Result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("legacy protocol = %q, want 2025-11-25", initialized.Result.ProtocolVersion)
	}
	notification := mcpRawRequest(fixture, http.MethodPost, "/mcp", []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`), "", "", nil)
	if notification.Code != http.StatusAccepted && notification.Code != http.StatusOK {
		t.Fatalf("legacy initialized notification status = %d, want 202 or 200", notification.Code)
	}

	list := mcpRawRequest(fixture, http.MethodPost, "/mcp", []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`), "", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("legacy tools/list status = %d body=%s", list.Code, list.Body.String())
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode legacy tools/list: %v", err)
	}
	if len(listed.Result.Tools) != 16 {
		t.Fatalf("no-form tools/list count = %d, want 16 ordinary tools", len(listed.Result.Tools))
	}
	for _, tool := range listed.Result.Tools {
		if tool.Name == "spaces.list" {
			t.Fatal("tools/list exposed removed spaces.list tool")
		}
		if tool.Name == "shares.create" || tool.Name == "files.move" {
			t.Fatalf("high-risk tool %q was exposed without form elicitation", tool.Name)
		}
	}
}

func TestMCPRawProtocolErrorsAndBoundaryRejections(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	tests := []struct {
		name       string
		method     string
		path       string
		body       []byte
		host       string
		origin     string
		extra      map[string]string
		wantStatus int
		bodyMatch  string
	}{
		{name: "parse error", method: http.MethodPost, path: "/mcp", body: []byte("{"), wantStatus: http.StatusBadRequest, bodyMatch: "malformed payload"},
		{name: "invalid request", method: http.MethodPost, path: "/mcp", body: []byte(`[]`), wantStatus: http.StatusBadRequest, bodyMatch: "malformed payload"},
		{name: "method not found", method: http.MethodPost, path: "/mcp", body: []byte(`{"jsonrpc":"2.0","id":3,"method":"does/not-exist"}`), wantStatus: http.StatusBadRequest, bodyMatch: "unsupported"},
		{name: "invalid params", method: http.MethodPost, path: "/mcp", body: []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":[]}`), wantStatus: http.StatusOK, bodyMatch: "-32602"},
		{name: "malformed protocol headers", method: http.MethodPost, path: "/mcp", body: []byte(`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{}}`), extra: map[string]string{"Mcp-Protocol-Version": "not-a-version", "Accept": "text/plain"}, wantStatus: http.StatusBadRequest},
		{name: "host rejection", method: http.MethodPost, path: "/mcp", body: []byte(`{}`), host: "other.example.test", wantStatus: http.StatusForbidden},
		{name: "origin rejection", method: http.MethodPost, path: "/mcp", body: []byte(`{}`), origin: "https://other.example.test", wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := mcpRawRequest(fixture, test.method, test.path, test.body, test.host, test.origin, test.extra)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.bodyMatch != "" && !strings.Contains(response.Body.String(), test.bodyMatch) {
				t.Fatalf("body %q does not contain %q", response.Body.String(), test.bodyMatch)
			}
		})
	}

	tooLarge := bytes.Repeat([]byte("x"), (1<<20)+1)
	response := mcpRawRequest(fixture, http.MethodPost, "/mcp", tooLarge, "", "", nil)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit status = %d, want 413", response.Code)
	}
}

func TestMCPStatelessMethodAndRouteBehavior(t *testing.T) {
	fixture := newMCPProtocolFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response := mcpRawRequest(fixture, method, "/mcp", nil, "", "", nil)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /mcp status = %d, want 405", method, response.Code)
		}
	}
	// Stateless servers do not establish or honor a session identifier. A
	// request with a caller-supplied ID still follows the normal POST path.
	initialize := []byte(`{"jsonrpc":"2.0","id":10,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"protocol-test","version":"1"}}}`)
	response := mcpRawRequest(fixture, http.MethodPost, "/mcp", initialize, "", "", map[string]string{"Mcp-Session-Id": "caller-supplied"})
	if response.Code != http.StatusOK {
		t.Fatalf("stateless POST with session header = %d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("stateless response unexpectedly established session %q", got)
	}

	transfer := mcpRawRequest(fixture, http.MethodGet, "/mcp/transfers/unknown", nil, "", "", nil)
	if transfer.Code != http.StatusUnauthorized {
		t.Fatalf("transfer route without bearer = %d, want 401", transfer.Code)
	}
}

func mcpRawRequest(fixture *mcpProtocolFixture, method, path string, body []byte, host, origin string, extra map[string]string) *httptest.ResponseRecorder {
	if host == "" {
		host = "mcp.example.test"
	}
	request := httptest.NewRequest(method, "http://"+host+path, bytes.NewReader(body))
	request.Host = host
	request.Header.Set("Authorization", "Bearer "+fixture.bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	for key, value := range extra {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}
