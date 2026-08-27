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
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/config"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/memberfiles"
	"omnora/internal/mountid"
	"omnora/internal/store"
	"omnora/internal/transfer"
	"omnora/internal/transferticket"
)

func TestDownloadRangeBudgetAndStatus(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		size      int64
		remaining int64
		start     int64
		length    int64
		status    int
		wantErr   error
	}{
		{name: "full 200", size: 10, remaining: 10, length: 10, status: http.StatusOK},
		{name: "partial 206", header: "bytes=2-5", size: 10, remaining: 4, start: 2, length: 4, status: http.StatusPartialContent},
		{name: "invalid 416", header: "bytes=99-", size: 10, remaining: 10, status: 0, wantErr: transfer.ErrUnsatisfiableRange},
		{name: "budget 416", header: "bytes=0-5", size: 10, remaining: 3, status: 0, wantErr: transfer.ErrRangeExceedsBudget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, length, status, err := downloadRange(tt.header, tt.size, tt.remaining)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("downloadRange() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || start != tt.start || length != tt.length || status != tt.status {
				t.Fatalf("downloadRange() = (%d,%d,%d,%v), want (%d,%d,%d,nil)", start, length, status, err, tt.start, tt.length, tt.status)
			}
		})
	}
}

func TestTransferBearerBindsURLAndRejectsQueryCredential(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/mcp/transfers/ticket-a", nil)
	request.Header.Set("Authorization", "Bearer ticket-a.secret")
	if _, ok := transferBearer(request); !ok {
		t.Fatal("matching URL and bearer should be accepted")
	}
	request = httptest.NewRequest(http.MethodGet, "/mcp/transfers/ticket-b", nil)
	request.SetPathValue("publicId", "ticket-b")
	request.Header.Set("Authorization", "Bearer ticket-a.secret")
	if _, ok := transferBearer(request); ok {
		t.Fatal("bearer for another ticket URL should be rejected")
	}
	request = httptest.NewRequest(http.MethodGet, "/mcp/transfers/ticket-a?secret=secret", nil)
	if !hasSecretQuery(request) {
		t.Fatal("query secret must be detected")
	}
}

func TestMCPTransferRouteGroupDisabled(t *testing.T) {
	handler := New(config.Config{Routes: map[domain.RouteGroup]bool{}}, nil)
	request := httptest.NewRequest(http.MethodGet, "/mcp/transfers/ticket-a", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || recorder.Body.String() == "" {
		t.Fatalf("disabled transfer route = %d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestMCPDownloadTicketLiveChecksAndRange(t *testing.T) {
	srv, db, principal, root := newMCPTransferFixture(t)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	locator := access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "notes.txt"}
	ticket, err := srv.transferTickets.IssueDownload(context.Background(), principal, locator, 10)
	if err != nil {
		t.Fatal(err)
	}
	headRequest := httptest.NewRequest(http.MethodHead, ticket.URL, nil)
	headRequest.Host = "mcp.example.test"
	headRequest.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	headRecorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(headRecorder, headRequest)
	if headRecorder.Code != http.StatusOK || headRecorder.Body.Len() != 0 || headRecorder.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD download = %d body=%d content-length=%q", headRecorder.Code, headRecorder.Body.Len(), headRecorder.Header().Get("Content-Length"))
	}
	request := httptest.NewRequest(http.MethodGet, ticket.URL, nil)
	request.Host = "mcp.example.test"
	request.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "0123456789" {
		t.Fatalf("download = %d %q", recorder.Code, recorder.Body.String())
	}

	partial, err := srv.transferTickets.IssueDownload(context.Background(), principal, locator, 4)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, partial.URL, nil)
	request.Host = "mcp.example.test"
	request.Header.Set("Authorization", "Bearer "+partial.BearerToken)
	request.Header.Set("Range", "bytes=2-5")
	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "2345" || recorder.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("partial download = %d %q range=%q", recorder.Code, recorder.Body.String(), recorder.Header().Get("Content-Range"))
	}
	invalidRangeTicket, err := srv.transferTickets.IssueDownload(context.Background(), principal, locator, 10)
	if err != nil {
		t.Fatal(err)
	}
	invalidRangeRequest := httptest.NewRequest(http.MethodGet, invalidRangeTicket.URL, nil)
	invalidRangeRequest.Host = "mcp.example.test"
	invalidRangeRequest.Header.Set("Authorization", "Bearer "+invalidRangeTicket.BearerToken)
	invalidRangeRequest.Header.Set("Range", "bytes=99-")
	invalidRangeRecorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(invalidRangeRecorder, invalidRangeRequest)
	if invalidRangeRecorder.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status = %d, want 416", invalidRangeRecorder.Code)
	}
	var invalidRangeMetadata string
	if err := db.SQL().QueryRow(`SELECT metadata_json FROM audit_events WHERE route_group = 'mcp' AND target_id = ? ORDER BY id DESC LIMIT 1`, invalidRangeTicket.PublicID).Scan(&invalidRangeMetadata); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(invalidRangeMetadata, `"bytes":0`) {
		t.Fatalf("failed range audit missing byte count: %s", invalidRangeMetadata)
	}

	queryRequest := httptest.NewRequest(http.MethodGet, ticket.URL+"?secret="+ticket.Secret, nil)
	queryRequest.Host = "mcp.example.test"
	queryRequest.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	queryRecorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(queryRecorder, queryRequest)
	if queryRecorder.Code != http.StatusBadRequest {
		t.Fatalf("query secret status = %d, want 400", queryRecorder.Code)
	}

	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("changed!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	driftRequest := httptest.NewRequest(http.MethodGet, ticket.URL, nil)
	driftRequest.Host = "mcp.example.test"
	driftRequest.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	driftRecorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(driftRecorder, driftRequest)
	if driftRecorder.Code != http.StatusConflict {
		t.Fatalf("etag drift status = %d, want 409", driftRecorder.Code)
	}

	if err := aitoken.NewService(db.SQL()).Revoke(context.Background(), principal.TokenID); err != nil {
		t.Fatal(err)
	}
	revokedRequest := httptest.NewRequest(http.MethodGet, partial.URL, nil)
	revokedRequest.Host = "mcp.example.test"
	revokedRequest.Header.Set("Authorization", "Bearer "+partial.BearerToken)
	revokedRecorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(revokedRecorder, revokedRequest)
	if revokedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", revokedRecorder.Code)
	}
	var auditCount int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM audit_events WHERE route_group = 'mcp' AND target_id = ?`, ticket.PublicID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 4 {
		t.Fatalf("transfer audit rows = %d, want intent/outcome rows", auditCount)
	}
	var metadata string
	if err := db.SQL().QueryRow(`SELECT metadata_json FROM audit_events WHERE route_group = 'mcp' AND target_id = ? ORDER BY id DESC LIMIT 1`, ticket.PublicID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, ticket.Secret) || strings.Contains(metadata, root) || strings.Contains(metadata, "Bearer") {
		t.Fatalf("transfer audit leaked credential/path: %s", metadata)
	}
	if !strings.Contains(metadata, `"targetLabel"`) || !strings.Contains(metadata, `"range"`) {
		t.Fatalf("transfer audit metadata missing target/range: %s", metadata)
	}
}

func TestMCPUploadTicketBudgetRetryAndClose(t *testing.T) {
	srv, db, principal, _ := newMCPTransferFixture(t)
	locator := access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "upload.txt"}
	upload, err := srv.memberFiles.PrepareUpload(context.Background(), access.Subject{AccountID: principal.AccountID, Principal: &principal}, memberfiles.UploadRequest{Locator: locator, ExpectedSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.transferTickets.IssueUpload(context.Background(), principal, upload.ID, locator, 5)
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/mcp/transfers/"+ticket.PublicID+"/parts/1", bytes.NewBufferString(body))
		request.Host = "mcp.example.test"
		request.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := put("hello"); recorder.Code != http.StatusNoContent {
		t.Fatalf("first part status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := put("hello"); recorder.Code != http.StatusNoContent {
		t.Fatalf("repeated part status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := put("toolong"); recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("budget/size status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	invalidPart := httptest.NewRequest(http.MethodPut, "/mcp/transfers/"+ticket.PublicID+"/parts/99", bytes.NewBufferString("x"))
	invalidPart.Host = "mcp.example.test"
	invalidPart.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	invalidPartRecorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(invalidPartRecorder, invalidPart)
	if invalidPartRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid part status = %d, want 400", invalidPartRecorder.Code)
	}
	if _, err := srv.memberFiles.CompleteUpload(context.Background(), access.Subject{AccountID: principal.AccountID, Principal: &principal}, upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := srv.transferTickets.CloseUploadTickets(context.Background(), upload.ID, transferticket.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if recorder := put("hello"); recorder.Code != http.StatusGone {
		t.Fatalf("closed upload ticket status = %d, want 410", recorder.Code)
	}
	var auditCount int
	if err := db.SQL().QueryRow(`SELECT COUNT(1) FROM audit_events WHERE route_group = 'mcp' AND target_id = ?`, ticket.PublicID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 6 {
		t.Fatalf("upload audit rows = %d, want intent/outcome for retries and close", auditCount)
	}
	var uploadMetadata string
	if err := db.SQL().QueryRow(`SELECT metadata_json FROM audit_events WHERE route_group = 'mcp' AND target_id = ? ORDER BY id DESC LIMIT 1`, ticket.PublicID).Scan(&uploadMetadata); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uploadMetadata, `"targetLabel"`) || !strings.Contains(uploadMetadata, `"part"`) {
		t.Fatalf("upload audit metadata missing target/part: %s", uploadMetadata)
	}
}

func TestMCPDownloadACLDegradeReturnsForbidden(t *testing.T) {
	srv, db, principal, root := newMCPTransferFixture(t)
	if err := os.WriteFile(filepath.Join(root, "acl.txt"), []byte("acl"), 0o600); err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.transferTickets.IssueDownload(context.Background(), principal, access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "acl.txt"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM mount_grants WHERE mount_id = 'mcp-transfer-mount' AND account_id = ?`, principal.AccountID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, ticket.URL, nil)
	request.Host = "mcp.example.test"
	request.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("ACL downgrade status = %d, want 403", recorder.Code)
	}
}

func newMCPTransferFixture(t *testing.T) (*Server, *store.DB, aitoken.Principal, string) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "mcp-transfer.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	admin, _ := createAPITestAccounts(t, db)
	root, err := os.MkdirTemp(".", ".mcp-transfer-mount-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`
INSERT INTO mounts(id,display_name,root_path,purpose,storage_kind,governance,mode,index_enabled,share_enabled,status,mount_identity_json)
VALUES ('mcp-transfer-mount','MCP Transfer',?,'common','external','normal','read_write',1,1,'active',?)
`, root, string(identityJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO mount_grants(mount_id,account_id,permission) VALUES ('mcp-transfer-mount',?,'editor')`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE route_groups SET enabled = 1 WHERE name = 'mcp'`); err != nil {
		t.Fatal(err)
	}
	issued, err := aitoken.NewService(db.SQL()).Create(context.Background(), aitoken.CreateRequest{
		AccountID: admin.ID, Name: "mcp-transfer", Scopes: []aitoken.Scope{aitoken.ScopeFilesDownloadTicket, aitoken.ScopeUploadsCreate},
		Boundaries: []aitoken.DirectoryBoundary{{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", RelativePath: "."}}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := aitoken.Principal{AccountID: issued.Token.AccountID, TokenID: issued.Token.ID, PublicID: issued.Token.PublicID, Scopes: issued.Token.Scopes, Boundaries: issued.Token.Boundaries, ExpiresAt: issued.Token.ExpiresAt}
	srv := NewServer(config.Config{Routes: map[domain.RouteGroup]bool{domain.RouteGroupMCP: true}, MCP: config.MCPConfig{AllowedHosts: []string{"mcp.example.test"}}}, db)
	return srv, db, principal, root
}

// setMCPDownloadSync installs a hook for the current test and restores the
// previous hook when the test finishes. Tests in this file run sequentially, so
// the package-private sync points are safe to steer per test.
func setMCPDownloadSync(t *testing.T, hook *func(), fn func()) {
	t.Helper()
	previous := *hook
	*hook = fn
	t.Cleanup(func() { *hook = previous })
}

func newMCPDownloadRequest(t *testing.T, ticket transferticket.IssuedTicket) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, ticket.URL, nil)
	request.Host = "mcp.example.test"
	request.Header.Set("Authorization", "Bearer "+ticket.BearerToken)
	return request
}

// assertObjectDriftDownload verifies a failed download response carries the
// 409 object_changed code and never set a success ETag or Content-Length.
func assertObjectDriftDownload(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusConflict {
		t.Fatalf("download status = %d, want 409 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"object_changed"`)) {
		t.Fatalf("download body = %s, want object_changed code", recorder.Body.String())
	}
	if etag := recorder.Header().Get("ETag"); etag != "" {
		t.Fatalf("drift download set ETag %q", etag)
	}
	if recorder.Header().Get("Content-Length") != "" {
		t.Fatalf("drift download set Content-Length %q", recorder.Header().Get("Content-Length"))
	}
}

// assertTicketUnconsumed verifies a failed download never consumed the ticket
// byte budget, so retries and accounting stay intact.
func assertTicketUnconsumed(t *testing.T, db *store.DB, ticket transferticket.IssuedTicket) {
	t.Helper()
	var consumed int64
	if err := db.SQL().QueryRow(`SELECT consumed_bytes FROM mcp_transfer_tickets WHERE id = ?`, ticket.ID).Scan(&consumed); err != nil {
		t.Fatalf("SELECT consumed_bytes: %v", err)
	}
	if consumed != 0 {
		t.Fatalf("drift download consumed %d bytes, want 0", consumed)
	}
}

// serveSwappedDownload issues a fresh download ticket for a freshly written
// notes.txt, installs a swap hook right after the first verification, and
// serves one request. The hook runs inside ServeHTTP, so the swap happens in
// the window between the first verification and the rooted open.
func serveSwappedDownload(t *testing.T, srv *Server, principal aitoken.Principal, root string, swap func(notesPath string) error) (*httptest.ResponseRecorder, transferticket.IssuedTicket) {
	t.Helper()
	notes := filepath.Join(root, "notes.txt")
	if err := os.Remove(notes); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove(notes) error = %v", err)
	}
	if err := os.WriteFile(notes, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("WriteFile(notes) error = %v", err)
	}
	ticket, err := srv.transferTickets.IssueDownload(context.Background(), principal, access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "notes.txt"}, 10)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	setMCPDownloadSync(t, &mcpDownloadSyncAfterFirstVerify, func() {
		if err := swap(notes); err != nil {
			t.Errorf("swap hook: %v", err)
		}
	})
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, newMCPDownloadRequest(t, ticket))
	return recorder, ticket
}

// Swapping the authorized file after the first verification must fail closed:
// whether the replacement is an external symlink or a different in-root file,
// the response is 409 object_changed with no bytes, no success headers and no
// byte-budget consumption.
func TestMCPDownloadRaceSwappedAfterFirstVerify(t *testing.T) {
	srv, db, principal, root := newMCPTransferFixture(t)

	t.Run("external symlink", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "probe.txt"), []byte("PROBE-SECRET"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "probe.txt"), filepath.Join(root, "replacement")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		recorder, ticket := serveSwappedDownload(t, srv, principal, root, func(notes string) error {
			return os.Rename(filepath.Join(root, "replacement"), notes)
		})
		assertObjectDriftDownload(t, recorder)
		assertTicketUnconsumed(t, db, ticket)
	})

	t.Run("in-root different file", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "evil.txt"), []byte("EVIL"), 0o600); err != nil {
			t.Fatal(err)
		}
		recorder, ticket := serveSwappedDownload(t, srv, principal, root, func(notes string) error {
			return os.Rename(filepath.Join(root, "evil.txt"), notes)
		})
		assertObjectDriftDownload(t, recorder)
		assertTicketUnconsumed(t, db, ticket)
	})
}

// This isolates the descriptor-binding check: the path is swapped to a
// different file before the rooted open and restored to the authorized inode
// before the final verification, so the final path-fingerprint replay passes.
// Only re-stating the already-open descriptor can detect that the served bytes
// would not be the authorized object.
func TestMCPDownloadRaceReplaceThenRestore(t *testing.T) {
	srv, db, principal, root := newMCPTransferFixture(t)
	notes := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notes, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(root, "evil.txt")
	if err := os.WriteFile(evil, []byte("EVIL"), 0o600); err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.transferTickets.IssueDownload(context.Background(), principal, access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "notes.txt"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	hold := filepath.Join(root, "hold.txt")
	setMCPDownloadSync(t, &mcpDownloadSyncAfterFirstVerify, func() {
		// Swap the authorized path to the attacker file before the open.
		if err := os.Rename(notes, hold); err != nil {
			t.Errorf("swap notes -> hold: %v", err)
		}
		if err := os.Rename(evil, notes); err != nil {
			t.Errorf("swap evil -> notes: %v", err)
		}
	})
	setMCPDownloadSync(t, &mcpDownloadSyncBeforeFinalVerify, func() {
		// Restore the authorized inode at the path before the final replay so
		// the path fingerprint still matches the ticket.
		if err := os.Rename(hold, notes); err != nil {
			t.Errorf("restore hold -> notes: %v", err)
		}
	})
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, newMCPDownloadRequest(t, ticket))
	assertObjectDriftDownload(t, recorder)
	assertTicketUnconsumed(t, db, ticket)

	var metadata string
	if err := db.SQL().QueryRow(`SELECT metadata_json FROM audit_events WHERE route_group = 'mcp' AND target_id = ? ORDER BY id DESC LIMIT 1`, ticket.PublicID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, ticket.Secret) || strings.Contains(metadata, root) || strings.Contains(metadata, "Bearer") {
		t.Fatalf("drift audit leaked credential/path: %s", metadata)
	}
}

// Revoking the token in the window after the first verification must be caught
// by the final verification and return 401, preserving the pre-existing
// revocation behavior even though the open already happened.
func TestMCPDownloadRaceTokenRevokedAfterFirstVerify(t *testing.T) {
	srv, db, principal, root := newMCPTransferFixture(t)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.transferTickets.IssueDownload(context.Background(), principal, access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "notes.txt"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	setMCPDownloadSync(t, &mcpDownloadSyncAfterFirstVerify, func() {
		if err := aitoken.NewService(db.SQL()).Revoke(context.Background(), principal.TokenID); err != nil {
			t.Errorf("revoke token: %v", err)
		}
	})
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, newMCPDownloadRequest(t, ticket))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked mid-download status = %d, want 401 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"unauthorized"`)) {
		t.Fatalf("revoked mid-download body = %s, want unauthorized code", recorder.Body.String())
	}
	assertTicketUnconsumed(t, db, ticket)
}

// Removing the mount grant in the window after the first verification must be
// caught by the final verification and return 403, preserving the ACL
// degradation behavior even though the open already happened.
func TestMCPDownloadRaceGrantRemovedAfterFirstVerify(t *testing.T) {
	srv, db, principal, root := newMCPTransferFixture(t)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	ticket, err := srv.transferTickets.IssueDownload(context.Background(), principal, access.Locator{Source: contentref.SourceCommonMount, MountID: "mcp-transfer-mount", Path: "notes.txt"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	setMCPDownloadSync(t, &mcpDownloadSyncAfterFirstVerify, func() {
		if _, err := db.SQL().Exec(`DELETE FROM mount_grants WHERE mount_id = 'mcp-transfer-mount' AND account_id = ?`, principal.AccountID); err != nil {
			t.Errorf("remove grant: %v", err)
		}
	})
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, newMCPDownloadRequest(t, ticket))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("grant removed mid-download status = %d, want 403 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"forbidden"`)) {
		t.Fatalf("grant removed mid-download body = %s, want forbidden code", recorder.Body.String())
	}
	assertTicketUnconsumed(t, db, ticket)
}
