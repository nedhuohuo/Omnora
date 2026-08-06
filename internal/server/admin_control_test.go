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

	"omnora/internal/identity"
)

func TestAdminCreateUser(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)

	createBody, err := json.Marshal(map[string]any{
		"email":       "new-user@example.test",
		"displayName": "New User",
		"password":    apiTestPassword,
		"role":        "member",
	})
	if err != nil {
		t.Fatalf("marshal create user request: %v", err)
	}

	memberReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(createBody))
	memberReq.Header.Set("Content-Type", "application/json")
	memberReq.AddCookie(memberCookie)
	memberRec := httptest.NewRecorder()
	handler.ServeHTTP(memberRec, memberReq)
	if memberRec.Code != http.StatusForbidden {
		t.Fatalf("create user as non-admin status = %d, want %d, body = %s", memberRec.Code, http.StatusForbidden, memberRec.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(createBody))
	adminReq.Header.Set("Content-Type", "application/json")
	adminReq.AddCookie(adminCookie)
	adminRec := httptest.NewRecorder()
	handler.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusCreated {
		t.Fatalf("create user as admin status = %d, body = %s", adminRec.Code, adminRec.Body.String())
	}
	var created struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(adminRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}
	if created.User.Email != "new-user@example.test" || created.User.Role != "member" {
		t.Fatalf("created user = %#v, want new-user@example.test/member", created.User)
	}

	listRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/users", adminCookie)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list admin users status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Items []adminUserDTO `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed admin users: %v", err)
	}
	found := false
	for _, item := range listed.Items {
		if item.ID == created.User.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected newly created user %q in admin user list %#v", created.User.ID, listed.Items)
	}

	svc := identity.New(db.SQL(), identity.Options{})
	if _, err := svc.Authenticate(context.Background(), "new-user@example.test", apiTestPassword); err != nil {
		t.Fatalf("expected new user to authenticate, got error: %v", err)
	}
}

func TestInitialAdminIsProtectedFromAccountAndSpaceMutations(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()

	decodeErrorCode := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
		}
		return body.Error.Code
	}

	disableReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+admin.ID+"/disable", nil)
	disableReq.AddCookie(adminCookie)
	disableRec := httptest.NewRecorder()
	handler.ServeHTTP(disableRec, disableReq)
	if disableRec.Code != http.StatusConflict || decodeErrorCode(disableRec) != "initial_admin_protected" {
		t.Fatalf("disable initial admin status = %d, body = %s", disableRec.Code, disableRec.Body.String())
	}

	var personalSpaceID string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT id FROM spaces WHERE kind = 'personal' AND owner_account_id = ?
`, admin.ID).Scan(&personalSpaceID); err != nil {
		t.Fatalf("load initial admin personal space: %v", err)
	}

	listMembersRec := authorizedAPITestRequest(t, handler, "/api/v1/admin/spaces/"+personalSpaceID+"/members", adminCookie)
	if listMembersRec.Code != http.StatusOK {
		t.Fatalf("list personal space members status = %d, body = %s", listMembersRec.Code, listMembersRec.Body.String())
	}
	var listed struct {
		Items []spaceMemberDTO `json:"items"`
	}
	if err := json.Unmarshal(listMembersRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode personal space members: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].AccountID != admin.ID || !listed.Items[0].Protected {
		t.Fatalf("personal space members = %#v, want protected initial admin", listed.Items)
	}

	permissionBody := bytes.NewReader([]byte(`{"permission":"viewer"}`))
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/"+personalSpaceID+"/members/"+admin.ID, permissionBody)
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(adminCookie)
	putRec := httptest.NewRecorder()
	handler.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusConflict || decodeErrorCode(putRec) != "initial_admin_protected" {
		t.Fatalf("change initial admin permission status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/spaces/"+personalSpaceID+"/members/"+admin.ID, nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict || decodeErrorCode(deleteRec) != "initial_admin_protected" {
		t.Fatalf("remove initial admin status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	var permission string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT permission FROM space_members WHERE space_id = ? AND account_id = ?
`, personalSpaceID, admin.ID).Scan(&permission); err != nil {
		t.Fatalf("load initial admin permission: %v", err)
	}
	if permission != "manager" {
		t.Fatalf("initial admin permission = %q, want manager", permission)
	}
}

func TestAdminPutSpaceMemberResolvesEmailAndRejectsUnknownAccount(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('shared-acl', 'shared', 'ACL Space', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('shared-acl', ?, 'manager')
`, admin.ID); err != nil {
		t.Fatalf("insert owner membership: %v", err)
	}

	body, err := json.Marshal(map[string]string{"permission": "manager"})
	if err != nil {
		t.Fatalf("marshal put member request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/shared-acl/members/"+strings.ToUpper(member.Email), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put member by email status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var added spaceMemberDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatalf("decode added member: %v", err)
	}
	if added.AccountID != member.ID || added.Email != member.Email || added.DisplayName != member.DisplayName || added.Permission != "manager" {
		t.Fatalf("added member = %#v, want resolved account with manager permission", added)
	}
	var permission string
	if err := db.SQL().QueryRowContext(ctx, `
SELECT permission FROM space_members WHERE space_id = 'shared-acl' AND account_id = ?
`, member.ID).Scan(&permission); err != nil {
		t.Fatalf("query added membership: %v", err)
	}
	if permission != "manager" {
		t.Fatalf("permission = %q, want manager", permission)
	}

	missingReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/shared-acl/members/missing@example.test", bytes.NewReader(body))
	missingReq.Header.Set("Content-Type", "application/json")
	missingReq.AddCookie(adminCookie)
	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("put missing member status = %d, want %d, body = %s", missingRec.Code, http.StatusNotFound, missingRec.Body.String())
	}

	missingSpaceReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/spaces/missing-space/members/"+member.ID, bytes.NewReader(body))
	missingSpaceReq.Header.Set("Content-Type", "application/json")
	missingSpaceReq.AddCookie(adminCookie)
	missingSpaceRec := httptest.NewRecorder()
	handler.ServeHTTP(missingSpaceRec, missingSpaceReq)
	if missingSpaceRec.Code != http.StatusNotFound {
		t.Fatalf("put member into missing space status = %d, want %d, body = %s", missingSpaceRec.Code, http.StatusNotFound, missingSpaceRec.Body.String())
	}
}

func TestAdminSpaceRenameAndDelete(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()
	const (
		spaceID = "shared-lifecycle"
		mountID = "mount-lifecycle"
	)

	root := createTestSpaceAndMount(t, db, spaceID, mountID, admin.ID, "read_write")
	keepFile := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keepFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write retained mount file: %v", err)
	}
	addSpaceMember(t, db, spaceID, member.ID, "viewer")

	for _, fixture := range []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "catalog entry",
			sql: `INSERT INTO catalog_entries(
				id, space_id, mount_id, relative_path, name, entry_kind, preview_kind,
				size_bytes, modified_at, identity_fingerprint
			) VALUES ('catalog-lifecycle', ?, ?, 'keep.txt', 'keep.txt', 'file', 'text', 4, CURRENT_TIMESTAMP, 'catalog-fingerprint')`,
			args: []any{spaceID, mountID},
		},
		{
			name: "file object",
			sql: `INSERT INTO file_objects(
				id, space_id, mount_id, relative_path, object_kind, size_bytes,
				modified_at, identity_fingerprint
			) VALUES ('file-lifecycle', ?, ?, 'keep.txt', 'file', 4, CURRENT_TIMESTAMP, 'file-fingerprint')`,
			args: []any{spaceID, mountID},
		},
		{
			name: "share",
			sql: `INSERT INTO shares(
				id, public_id, secret_hash, creator_account_id, space_id, mount_id,
				relative_path, expires_at
			) VALUES ('share-lifecycle', 'public-lifecycle', 'share-hash', ?, ?, ?, 'keep.txt', '2099-01-01T00:00:00Z')`,
			args: []any{admin.ID, spaceID, mountID},
		},
		{
			name: "share session",
			sql: `INSERT INTO share_sessions(id, share_id, session_hash, generation, expires_at)
				VALUES ('share-session-lifecycle', 'share-lifecycle', 'share-session-hash', 1, '2099-01-01T00:00:00Z')`,
		},
		{
			name: "AI token",
			sql: `INSERT INTO ai_tokens(
				id, public_id, secret_hash, account_id, name, scopes, created_at, expires_at, updated_at
			) VALUES ('token-lifecycle', 'ait-lifecycle', 'token-hash', ?, 'Lifecycle token', '["spaces:read"]', CURRENT_TIMESTAMP, '2099-01-01T00:00:00Z', CURRENT_TIMESTAMP)`,
			args: []any{admin.ID},
		},
		{
			name: "AI token boundary",
			sql: `INSERT INTO ai_token_boundaries(token_id, space_id, mount_id, relative_path)
				VALUES ('token-lifecycle', ?, ?, '')`,
			args: []any{spaceID, mountID},
		},
		{
			name: "upload session",
			sql: `INSERT INTO upload_sessions(
				id, account_id, space_id, mount_id, target_relative_path, declared_size,
				part_size, temp_dir, expires_at
			) VALUES ('upload-lifecycle', ?, ?, ?, 'incoming.bin', 4, 4, ?, '2099-01-01T00:00:00Z')`,
			args: []any{admin.ID, spaceID, mountID, filepath.Join(root, ".omnora", "tmp", "uploads")},
		},
		{
			name: "upload part",
			sql: `INSERT INTO upload_parts(upload_id, part_number, size_bytes)
				VALUES ('upload-lifecycle', 1, 4)`,
		},
		{
			name: "catalog job",
			sql: `INSERT INTO jobs(id, kind, payload_json)
				VALUES ('job-lifecycle', 'catalog_scan', '{"mount_id":"mount-lifecycle"}')`,
		},
		{
			name: "nested catalog job",
			sql: `INSERT INTO jobs(id, kind, payload_json)
				VALUES ('job-lifecycle-nested', 'catalog_scan', '{"details":{"mount_id":"mount-lifecycle"}}')`,
		},
		{
			name: "unrelated job",
			sql: `INSERT INTO jobs(id, kind, payload_json)
				VALUES ('job-lifecycle-unrelated', 'catalog_scan', '{"note":"mount-lifecycle"}')`,
		},
	} {
		if _, err := db.SQL().ExecContext(ctx, fixture.sql, fixture.args...); err != nil {
			t.Fatalf("insert %s fixture: %v", fixture.name, err)
		}
	}

	renameReq := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/spaces/"+spaceID, bytes.NewReader([]byte(`{"name":" Renamed Space "}`)))
	renameReq.Header.Set("Content-Type", "application/json")
	renameReq.AddCookie(adminCookie)
	renameRec := httptest.NewRecorder()
	handler.ServeHTTP(renameRec, renameReq)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("rename shared space status = %d, body = %s", renameRec.Code, renameRec.Body.String())
	}
	var renamed struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(renameRec.Body.Bytes(), &renamed); err != nil {
		t.Fatalf("decode renamed space: %v", err)
	}
	if renamed.ID != spaceID || renamed.Type != "shared" || renamed.Name != "Renamed Space" {
		t.Fatalf("renamed space = %#v", renamed)
	}
	var persistedName string
	if err := db.SQL().QueryRowContext(ctx, `SELECT name FROM spaces WHERE id = ?`, spaceID).Scan(&persistedName); err != nil {
		t.Fatalf("load renamed space: %v", err)
	}
	if persistedName != "Renamed Space" {
		t.Fatalf("persisted space name = %q, want Renamed Space", persistedName)
	}

	mismatchReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/spaces/"+spaceID, bytes.NewReader([]byte(`{"name":"Test Space"}`)))
	mismatchReq.Header.Set("Content-Type", "application/json")
	mismatchReq.AddCookie(adminCookie)
	mismatchRec := httptest.NewRecorder()
	handler.ServeHTTP(mismatchRec, mismatchReq)
	if mismatchRec.Code != http.StatusConflict || decodeAdminControlErrorCode(t, mismatchRec) != "confirmation_required" {
		t.Fatalf("mismatched confirmation status = %d, body = %s", mismatchRec.Code, mismatchRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/spaces/"+spaceID, bytes.NewReader([]byte(`{"name":"Renamed Space"}`)))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete shared space status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	var deleted struct {
		ID          string `json:"id"`
		Deleted     bool   `json:"deleted"`
		DeleteData  bool   `json:"deleteData"`
		DataDeleted bool   `json:"dataDeleted"`
	}
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode deleted space: %v", err)
	}
	if deleted.ID != spaceID || !deleted.Deleted || deleted.DeleteData || deleted.DataDeleted {
		t.Fatalf("deleted space response = %#v", deleted)
	}

	for _, check := range []struct {
		name  string
		query string
		args  []any
		want  int
	}{
		{name: "space", query: `SELECT COUNT(1) FROM spaces WHERE id = ?`, args: []any{spaceID}},
		{name: "members", query: `SELECT COUNT(1) FROM space_members WHERE space_id = ?`, args: []any{spaceID}},
		{name: "mount", query: `SELECT COUNT(1) FROM mounts WHERE id = ?`, args: []any{mountID}},
		{name: "catalog entries", query: `SELECT COUNT(1) FROM catalog_entries WHERE space_id = ?`, args: []any{spaceID}},
		{name: "file objects", query: `SELECT COUNT(1) FROM file_objects WHERE space_id = ?`, args: []any{spaceID}},
		{name: "shares", query: `SELECT COUNT(1) FROM shares WHERE space_id = ?`, args: []any{spaceID}},
		{name: "share sessions", query: `SELECT COUNT(1) FROM share_sessions WHERE id = 'share-session-lifecycle'`},
		{name: "AI token boundaries", query: `SELECT COUNT(1) FROM ai_token_boundaries WHERE space_id = ?`, args: []any{spaceID}},
		{name: "upload sessions", query: `SELECT COUNT(1) FROM upload_sessions WHERE space_id = ?`, args: []any{spaceID}},
		{name: "upload parts", query: `SELECT COUNT(1) FROM upload_parts WHERE upload_id = 'upload-lifecycle'`},
		{name: "AI token", query: `SELECT COUNT(1) FROM ai_tokens WHERE id = 'token-lifecycle'`, want: 1},
	} {
		var count int
		if err := db.SQL().QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("count %s after space deletion: %v", check.name, err)
		}
		if count != check.want {
			t.Fatalf("%s count after space deletion = %d, want %d", check.name, count, check.want)
		}
	}

	for _, jobID := range []string{"job-lifecycle", "job-lifecycle-nested"} {
		var jobStatus string
		if err := db.SQL().QueryRowContext(ctx, `SELECT status FROM jobs WHERE id = ?`, jobID).Scan(&jobStatus); err != nil {
			t.Fatalf("load catalog job %s after space deletion: %v", jobID, err)
		}
		if jobStatus != "canceled" {
			t.Fatalf("catalog job %s status after space deletion = %q, want canceled", jobID, jobStatus)
		}
	}
	var unrelatedJobStatus string
	if err := db.SQL().QueryRowContext(ctx, `SELECT status FROM jobs WHERE id = 'job-lifecycle-unrelated'`).Scan(&unrelatedJobStatus); err != nil {
		t.Fatalf("load unrelated job after space deletion: %v", err)
	}
	if unrelatedJobStatus != "queued" {
		t.Fatalf("unrelated job status after space deletion = %q, want queued", unrelatedJobStatus)
	}
	var auditMetadata string
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT metadata_json
		FROM audit_events
		WHERE action = 'admin_space_delete' AND target_type = 'space' AND target_id = ?
		ORDER BY id DESC
		LIMIT 1
	`, spaceID).Scan(&auditMetadata); err != nil {
		t.Fatalf("load space deletion audit: %v", err)
	}
	if !strings.Contains(auditMetadata, "Renamed Space") || !strings.Contains(auditMetadata, `"dataDeleted":false`) {
		t.Fatalf("space deletion audit metadata = %s", auditMetadata)
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("expected mounted file to remain after space deletion: %v", err)
	}
}

func TestAdminSpaceProtection(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	memberCookie := issueAPITestSession(t, db, member.ID)
	ctx := context.Background()

	if _, err := db.SQL().ExecContext(ctx, `
		INSERT INTO spaces(id, kind, name, owner_account_id, status)
		VALUES ('shared-protection', 'shared', 'Protected Test', ?, 'active')
	`, admin.ID); err != nil {
		t.Fatalf("insert shared space: %v", err)
	}
	var personalSpaceID, personalSpaceName string
	if err := db.SQL().QueryRowContext(ctx, `
		SELECT id, name
		FROM spaces
		WHERE kind = 'personal' AND owner_account_id = ?
	`, admin.ID).Scan(&personalSpaceID, &personalSpaceName); err != nil {
		t.Fatalf("load admin personal space: %v", err)
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		cookie *http.Cookie
		code   string
	}{
		{
			name: "member cannot rename shared space", method: http.MethodPatch,
			path: "/api/v1/admin/spaces/shared-protection", body: `{"name":"Renamed"}`,
			cookie: memberCookie,
		},
		{
			name: "member cannot delete shared space", method: http.MethodDelete,
			path: "/api/v1/admin/spaces/shared-protection", body: `{"name":"Protected Test"}`,
			cookie: memberCookie,
		},
		{
			name: "personal space cannot be renamed", method: http.MethodPatch,
			path: "/api/v1/admin/spaces/" + personalSpaceID, body: `{"name":"Renamed"}`,
			cookie: adminCookie, code: "personal_space_protected",
		},
		{
			name: "personal space cannot be deleted", method: http.MethodDelete,
			path: "/api/v1/admin/spaces/" + personalSpaceID, body: `{"name":` + mustMarshalJSONString(t, personalSpaceName) + `}`,
			cookie: adminCookie, code: "personal_space_protected",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(tc.cookie)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tc.code == "" {
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusConflict || decodeAdminControlErrorCode(t, rec) != tc.code {
				t.Fatalf("status = %d, code = %q, body = %s", rec.Code, decodeAdminControlErrorCode(t, rec), rec.Body.String())
			}
		})
	}

	invalidNameReq := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/spaces/shared-protection", strings.NewReader(`{"name":"bad`+string(rune(0x85))+`name"}`))
	invalidNameReq.Header.Set("Content-Type", "application/json")
	invalidNameReq.AddCookie(adminCookie)
	invalidNameRec := httptest.NewRecorder()
	handler.ServeHTTP(invalidNameRec, invalidNameReq)
	if invalidNameRec.Code != http.StatusBadRequest {
		t.Fatalf("C1 control character rename status = %d, body = %s", invalidNameRec.Code, invalidNameRec.Body.String())
	}
}

func decodeAdminControlErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
	}
	return body.Error.Code
}

func mustMarshalJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON string: %v", err)
	}
	return string(encoded)
}

func TestAuditEventsReturnReadableActorAndTargetLabels(t *testing.T) {
	db, handler := newAPITestServer(t)
	admin, member := createAPITestAccounts(t, db)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	ctx := context.Background()

	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO backups(id, status, path, created_by, created_at, notes)
VALUES ('bkp-readable', 'failed', NULL, ?, '2026-08-04T15:00:00Z', 'sqlite online backup')
`, admin.ID); err != nil {
		t.Fatalf("insert backup: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO audit_events(actor_account_id, route_group, action, target_type, target_id, metadata_json)
VALUES
	(?, 'rest', 'admin_user_disable', 'account', ?, '{}'),
	(NULL, 'rest', 'route_group_update', 'route_group', 'mcp', '{}'),
	(?, 'rest', 'admin_backup_create', 'backup', 'bkp-readable', '{}')
`, admin.ID, member.ID, admin.ID); err != nil {
		t.Fatalf("insert audit events: %v", err)
	}

	rec := authorizedAPITestRequest(t, handler, "/api/v1/audit/events?limit=10", adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list audit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Items []auditEventDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode audit events: %v", err)
	}
	if len(listed.Items) < 2 {
		t.Fatalf("audit events = %#v, want inserted rows", listed.Items)
	}
	var accountEvent auditEventDTO
	var systemEvent auditEventDTO
	var backupEvent auditEventDTO
	for _, item := range listed.Items {
		switch item.Action {
		case "admin_user_disable":
			accountEvent = item
		case "route_group_update":
			systemEvent = item
		case "admin_backup_create":
			backupEvent = item
		}
	}
	if accountEvent.Actor != admin.ID || accountEvent.ActorLabel != "Admin" || accountEvent.ActorEmail != admin.Email || accountEvent.ActorDisplayName != admin.DisplayName {
		t.Fatalf("account audit actor fields = %#v, want readable admin labels", accountEvent)
	}
	if accountEvent.TargetID != member.ID || accountEvent.TargetLabel != "Member" {
		t.Fatalf("account audit target fields = %#v, want member target label", accountEvent)
	}
	if systemEvent.ActorLabel != "system" || systemEvent.TargetLabel != "mcp" {
		t.Fatalf("system audit labels = %#v, want system / mcp", systemEvent)
	}
	if backupEvent.TargetID != "bkp-readable" || backupEvent.TargetLabel != "backup 2026-08-04T15:00:00Z" {
		t.Fatalf("backup audit target fields = %#v, want readable backup label", backupEvent)
	}
}
