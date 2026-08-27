package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func TestMemberAITokenCreateAndListUseAccountContentBoundaries(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, member.ID)
	insertAITokenCommonMount(t, db, member.ID, "common-token", "Team files", domain.ContentPermissionViewer, domain.MountModeReadOnly)

	response := postMemberAIToken(t, handler, cookie, `{
		"name":"account content",
		"scopes":["mounts:read","files:list"],
		"boundaries":[
			{"source":"all_account_content"},
			{"source":"personal","path":"docs"},
			{"source":"common_mount","mountId":"common-token","path":"shared"}
		]
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create AI token status = %d, body = %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/ai-tokens", nil)
	request.AddCookie(cookie)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, request)
	if listed.Code != http.StatusOK {
		t.Fatalf("list AI tokens status = %d, body = %s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), "personal-default") || strings.Contains(listed.Body.String(), "spaceId") {
		t.Fatalf("AI token list leaked a protected or Space locator: %s", listed.Body.String())
	}
	var payload struct {
		Items []struct {
			Scopes     []string         `json:"scopes"`
			Boundaries []map[string]any `json:"boundaries"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode AI token list: %v", err)
	}
	if len(payload.Items) != 1 || len(payload.Items[0].Boundaries) != 3 {
		t.Fatalf("AI token list = %#v", payload.Items)
	}
	want := map[string]map[string]any{
		"all_account_content": {"source": "all_account_content"},
		"personal":            {"source": "personal", "path": "docs"},
		"common_mount":        {"source": "common_mount", "mountId": "common-token", "mountName": "Team files", "path": "shared"},
	}
	for _, boundary := range payload.Items[0].Boundaries {
		source, _ := boundary["source"].(string)
		expected, ok := want[source]
		if !ok {
			t.Fatalf("unexpected boundary %#v", boundary)
		}
		encoded, _ := json.Marshal(boundary)
		wantEncoded, _ := json.Marshal(expected)
		if string(encoded) != string(wantEncoded) {
			t.Errorf("boundary %q = %s, want %s", source, encoded, wantEncoded)
		}
	}
}

func TestMemberAITokenCreateRejectsLegacyBoundaryFields(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, member.ID)

	for _, field := range []string{
		`"spaceId":"space-old"`,
		`"space_id":"space-old"`,
		`"accountId":"acct-other"`,
		`"defaultMountId":"personal-default"`,
		`"collaborationId":"collab-old"`,
		`"mount_id":"mount-old"`,
		`"relativePath":"docs"`,
	} {
		t.Run(field, func(t *testing.T) {
			body := `{"name":"legacy","scopes":["files:list"],"boundaries":[{"source":"personal",` + field + `}]}`
			response := postMemberAIToken(t, handler, cookie, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_json") {
				t.Fatalf("legacy field %s status/body = %d/%s", field, response.Code, response.Body.String())
			}
		})
	}
	response := postMemberAIToken(t, handler, cookie, `{
		"name":"legacy expiry",
		"scopes":["files:list"],
		"boundaries":[{"source":"personal"}],
		"expires_at":"2099-01-01T00:00:00Z"
	}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_json") {
		t.Fatalf("legacy expires_at status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestMemberAITokenWriteScopesRequireNarrowEditorBoundary(t *testing.T) {
	db, handler := newAPITestServer(t)
	_, member := createAPITestAccounts(t, db)
	cookie := issueAPITestSession(t, db, member.ID)
	insertAITokenCommonMount(t, db, member.ID, "common-viewer", "Viewer", domain.ContentPermissionViewer, domain.MountModeReadOnly)
	insertAITokenCommonMount(t, db, member.ID, "common-editor", "Editor", domain.ContentPermissionEditor, domain.MountModeReadWrite)

	for _, test := range []struct {
		name       string
		boundary   string
		wantStatus int
	}{
		{name: "all account content", boundary: `{"source":"all_account_content"}`, wantStatus: http.StatusBadRequest},
		{name: "viewer common mount", boundary: `{"source":"common_mount","mountId":"common-viewer"}`, wantStatus: http.StatusForbidden},
		{name: "editor common mount", boundary: `{"source":"common_mount","mountId":"common-editor"}`, wantStatus: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"name":"write boundary","scopes":["files:write"],"boundaries":[` + test.boundary + `]}`
			response := postMemberAIToken(t, handler, cookie, body)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func postMemberAIToken(t *testing.T, handler http.Handler, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai-tokens", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func insertAITokenCommonMount(t *testing.T, db *store.DB, accountID, mountID, displayName string, permission domain.ContentPermission, mode domain.MountMode) {
	t.Helper()
	root := filepath.Join(t.TempDir(), mountID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	identityValue, err := mountid.Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	identityJSON, err := json.Marshal(identityValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json)
VALUES (?, ?, ?, 'common', 'external', 'normal', ?, 'active', ?)
`, mountID, displayName, root, mode, string(identityJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `
INSERT INTO mount_grants(mount_id, account_id, permission) VALUES (?, ?, ?)
`, mountID, accountID, permission); err != nil {
		t.Fatal(err)
	}
}
