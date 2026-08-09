package transferticket

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

type ticketFixture struct {
	db        *sql.DB
	root      string
	mountID   string
	account   string
	principal aitoken.Principal
	bearer    string
	clock     *time.Time
}

func newTicketFixture(t *testing.T) ticketFixture {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "ticket.db")})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	root, err := os.MkdirTemp(".", ".ticket-mount-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs(root) error = %v", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root) error = %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("Marshal(identity) error = %v", err)
	}
	db := handle.SQL()
	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-ticket', 'ticket@example.test', 'Ticket', 'member', 'active');
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json)
VALUES ('mount-ticket', 'Files', ?, 'common', 'external', 'normal', 'read_write', 'active', ?);
INSERT INTO mount_grants(mount_id, account_id, permission)
VALUES ('mount-ticket', 'acct-ticket', 'editor');
`, root, string(identityJSON)); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	tokens := aitoken.NewService(db, aitoken.WithClock(func() time.Time { return now }))
	issued, err := tokens.Create(context.Background(), aitoken.CreateRequest{
		AccountID: "acct-ticket",
		Name:      "ticket",
		Scopes: []aitoken.Scope{
			aitoken.ScopeFilesDownloadTicket,
			aitoken.ScopeUploadsCreate,
		},
		Boundaries: []aitoken.DirectoryBoundary{{Source: contentref.SourceCommonMount, MountID: "mount-ticket", RelativePath: "."}},
		ExpiresAt:  now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create(token) error = %v", err)
	}
	principal := aitoken.Principal{
		AccountID:  issued.Token.AccountID,
		TokenID:    issued.Token.ID,
		PublicID:   issued.Token.PublicID,
		Scopes:     issued.Token.Scopes,
		Boundaries: issued.Token.Boundaries,
		ExpiresAt:  issued.Token.ExpiresAt,
	}
	return ticketFixture{
		db: db, root: root, mountID: "mount-ticket", account: "acct-ticket",
		principal: principal, bearer: issued.BearerToken, clock: &now,
	}
}

func (f ticketFixture) service(t *testing.T, extra ...Option) *Service {
	t.Helper()
	tokens := aitoken.NewService(f.db, aitoken.WithClock(func() time.Time { return *f.clock }))
	guard := access.NewGuard(f.db)
	options := []Option{WithClock(func() time.Time { return *f.clock })}
	options = append(options, extra...)
	return NewService(f.db, tokens, guard, options...)
}

func (f ticketFixture) locator(path string) access.Locator {
	return access.Locator{Source: contentref.SourceCommonMount, MountID: f.mountID, Path: path}
}

func (f ticketFixture) issueUploadSession(t *testing.T, id, target string, expires time.Time) {
	t.Helper()
	_, err := f.db.Exec(`
INSERT INTO upload_sessions(id, account_id, mount_id, target_relative_path, declared_size, part_size, temp_dir, expires_at)
VALUES (?, ?, ?, ?, 100, 32, ?, ?)
`, id, f.account, f.mountID, target, filepath.Join(f.root, ".omnora", "tmp"), expires.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("insert upload session: %v", err)
	}
}

func TestIssueDownloadStoresOnlySecretHashAndClampsTokenExpiry(t *testing.T) {
	f := newTicketFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	s := f.service(t)
	ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("notes.txt"), 10)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	if ticket.Secret == "" || ticket.PublicID == "" || ticket.URL != "/mcp/transfers/"+ticket.PublicID || ticket.TicketURL != ticket.URL {
		t.Fatalf("issued ticket = %#v", ticket)
	}
	if ticket.ExpiresAt.Sub(*f.clock) != 10*time.Minute {
		t.Fatalf("download TTL = %s, want 10m", ticket.ExpiresAt.Sub(*f.clock))
	}
	if ticket.Size != int64(len("hello")) {
		t.Fatalf("download size = %d, want %d", ticket.Size, len("hello"))
	}
	var storedHash string
	if err := f.db.QueryRow(`SELECT secret_hash FROM mcp_transfer_tickets WHERE id = ?`, ticket.ID).Scan(&storedHash); err != nil {
		t.Fatalf("read secret hash: %v", err)
	}
	if storedHash == ticket.Secret || storedHash == "" || storedHash != hashSecret(ticket.Secret) {
		t.Fatalf("secret hash = %q, secret = %q", storedHash, ticket.Secret)
	}
	verified, err := s.Verify(context.Background(), "Bearer "+ticket.BearerToken, OperationDownload)
	if err != nil {
		t.Fatalf("Verify(download) error = %v", err)
	}
	if verified.ID != ticket.ID || verified.Locator.Path != "notes.txt" || verified.MaxBytes != 10 {
		t.Fatalf("verified ticket = %#v", verified)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationUpload); !errors.Is(err, ErrWrongOperation) {
		t.Fatalf("Verify(wrong operation) error = %v, want %v", err, ErrWrongOperation)
	}
	if err := s.AddBytes(context.Background(), ticket.ID, 6); err != nil {
		t.Fatalf("AddBytes(6) error = %v", err)
	}
	if err := s.AddBytes(context.Background(), ticket.ID, 4); err != nil {
		t.Fatalf("AddBytes(4) error = %v", err)
	}
	if err := s.AddBytes(context.Background(), ticket.ID, 1); !errors.Is(err, ErrByteBudgetExceeded) {
		t.Fatalf("AddBytes(overflow) error = %v, want %v", err, ErrByteBudgetExceeded)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); err != nil {
		t.Fatalf("download ticket was not reusable: %v", err)
	}
}

func TestIssueDownloadZeroBudgetUsesObjectSize(t *testing.T) {
	f := newTicketFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "bounded.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	s := f.service(t)
	ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("bounded.txt"), 0)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	if ticket.MaxBytes != 5 || ticket.Size != 5 {
		t.Fatalf("zero-budget ticket = max %d size %d, want 5/5", ticket.MaxBytes, ticket.Size)
	}
	if err := s.AddBytes(context.Background(), ticket.ID, 5); err != nil {
		t.Fatalf("AddBytes(exact size) error = %v", err)
	}
	if err := s.AddBytes(context.Background(), ticket.ID, 1); !errors.Is(err, ErrByteBudgetExceeded) {
		t.Fatalf("AddBytes(over budget) error = %v, want %v", err, ErrByteBudgetExceeded)
	}
}

func TestIssueDownloadClampsToShorterAITokenExpiry(t *testing.T) {
	f := newTicketFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "short-token.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	shortExpiry := (*f.clock).Add(2 * time.Minute)
	if _, err := f.db.Exec(`UPDATE ai_tokens SET expires_at = ? WHERE id = ?`, shortExpiry.Format(time.RFC3339Nano), f.principal.TokenID); err != nil {
		t.Fatalf("shorten token expiry: %v", err)
	}
	s := f.service(t)
	ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("short-token.txt"), 10)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	if !ticket.ExpiresAt.Equal(shortExpiry) {
		t.Fatalf("download expiry = %s, want token expiry %s", ticket.ExpiresAt, shortExpiry)
	}
}

func TestCloseUploadTicketsClosesAllActiveTicketsForSession(t *testing.T) {
	f := newTicketFixture(t)
	f.issueUploadSession(t, "upload-close-many", "pending.bin", (*f.clock).Add(time.Hour))
	s := f.service(t)
	first, err := s.IssueUpload(context.Background(), f.principal, "upload-close-many", f.locator("pending.bin"), 100)
	if err != nil {
		t.Fatalf("IssueUpload(first) error = %v", err)
	}
	second, err := s.IssueUpload(context.Background(), f.principal, "upload-close-many", f.locator("pending.bin"), 100)
	if err != nil {
		t.Fatalf("IssueUpload(second) error = %v", err)
	}
	if err := s.CloseUploadTickets(context.Background(), "upload-close-many", StatusCanceled); err != nil {
		t.Fatalf("CloseUploadTickets() error = %v", err)
	}
	for _, ticket := range []IssuedTicket{first, second} {
		var status string
		if err := f.db.QueryRow(`SELECT status FROM mcp_transfer_tickets WHERE id = ?`, ticket.ID).Scan(&status); err != nil {
			t.Fatalf("read ticket status: %v", err)
		}
		if status != string(StatusCanceled) {
			t.Fatalf("ticket %s status = %q, want %q", ticket.ID, status, StatusCanceled)
		}
	}
}

func TestIssueUploadClampsUploadExpiryAndCloseIsOneShot(t *testing.T) {
	f := newTicketFixture(t)
	expires := (*f.clock).Add(5 * time.Minute)
	f.issueUploadSession(t, "upload-ticket", "incoming.bin", expires)
	s := f.service(t)
	ticket, err := s.IssueUpload(context.Background(), f.principal, "upload-ticket", f.locator("incoming.bin"), 100)
	if err != nil {
		t.Fatalf("IssueUpload() error = %v", err)
	}
	if !ticket.ExpiresAt.Equal(expires) {
		t.Fatalf("upload expiry = %s, want %s", ticket.ExpiresAt, expires)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationUpload); err != nil {
		t.Fatalf("Verify(upload) error = %v", err)
	}
	if err := s.Close(context.Background(), ticket.ID, StatusCompleted); err != nil {
		t.Fatalf("Close(completed) error = %v", err)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationUpload); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("Verify(closed) error = %v, want %v", err, ErrTicketClosed)
	}
	if err := s.Close(context.Background(), ticket.ID, StatusCanceled); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("Close(replay) error = %v, want %v", err, ErrTicketClosed)
	}
}

func TestVerifyRejectsObjectDriftRevocationAndACLChange(t *testing.T) {
	f := newTicketFixture(t)
	file := filepath.Join(f.root, "drift.txt")
	if err := os.WriteFile(file, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	s := f.service(t)
	ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("drift.txt"), 100)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	if err := os.WriteFile(file, []byte("after with another size"), 0o644); err != nil {
		t.Fatalf("replace file: %v", err)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrObjectDrift) {
		t.Fatalf("Verify(object drift) error = %v, want %v", err, ErrObjectDrift)
	}

	if err := os.WriteFile(file, []byte("stable"), 0o644); err != nil {
		t.Fatalf("reset file: %v", err)
	}
	ticket, err = s.IssueDownload(context.Background(), f.principal, f.locator("drift.txt"), 100)
	if err != nil {
		t.Fatalf("IssueDownload(second) error = %v", err)
	}
	if _, err := f.db.Exec(`UPDATE ai_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, f.principal.TokenID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Verify(revoked token) error = %v, want unauthorized", err)
	}
}

func TestVerifyFailsClosedForInactiveAccountBoundaryAndMountDrift(t *testing.T) {
	t.Run("inactive account", func(t *testing.T) {
		f := newTicketFixture(t)
		file := filepath.Join(f.root, "inactive.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		s := f.service(t)
		ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("inactive.txt"), 10)
		if err != nil {
			t.Fatalf("IssueDownload() error = %v", err)
		}
		if _, err := f.db.Exec(`UPDATE accounts SET status = 'disabled' WHERE id = ?`, f.account); err != nil {
			t.Fatalf("disable account: %v", err)
		}
		if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Verify(disabled account) error = %v, want unauthorized", err)
		}
	})

	t.Run("boundary downgrade", func(t *testing.T) {
		f := newTicketFixture(t)
		file := filepath.Join(f.root, "boundary.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		s := f.service(t)
		ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("boundary.txt"), 10)
		if err != nil {
			t.Fatalf("IssueDownload() error = %v", err)
		}
		if _, err := f.db.Exec(`DELETE FROM ai_token_boundaries WHERE token_id = ?`, f.principal.TokenID); err != nil {
			t.Fatalf("delete boundary: %v", err)
		}
		if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Verify(boundary downgrade) error = %v, want unauthorized", err)
		}
	})

	t.Run("ACL downgrade", func(t *testing.T) {
		f := newTicketFixture(t)
		file := filepath.Join(f.root, "acl.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		s := f.service(t)
		ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("acl.txt"), 10)
		if err != nil {
			t.Fatalf("IssueDownload() error = %v", err)
		}
		if _, err := f.db.Exec(`DELETE FROM mount_grants WHERE mount_id = ? AND account_id = ?`, f.mountID, f.account); err != nil {
			t.Fatalf("delete ACL: %v", err)
		}
		if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Verify(ACL downgrade) error = %v, want unauthorized", err)
		}
	})

	t.Run("mount identity drift", func(t *testing.T) {
		f := newTicketFixture(t)
		file := filepath.Join(f.root, "mount-drift.txt")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		s := f.service(t)
		ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("mount-drift.txt"), 10)
		if err != nil {
			t.Fatalf("IssueDownload() error = %v", err)
		}
		otherRoot, err := os.MkdirTemp(".", ".ticket-drift-")
		if err != nil {
			t.Fatalf("MkdirTemp(other root) error = %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(otherRoot) })
		otherRoot, err = filepath.Abs(otherRoot)
		if err != nil {
			t.Fatalf("Abs(other root) error = %v", err)
		}
		otherIdentity, err := mountid.Capture(otherRoot)
		if err != nil {
			t.Fatalf("Capture(other root) error = %v", err)
		}
		identityJSON, err := json.Marshal(otherIdentity)
		if err != nil {
			t.Fatalf("Marshal(other identity) error = %v", err)
		}
		if _, err := f.db.Exec(`UPDATE mounts SET mount_identity_json = ? WHERE id = ?`, string(identityJSON), f.mountID); err != nil {
			t.Fatalf("update mount identity: %v", err)
		}
		if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Verify(mount drift) error = %v, want unauthorized", err)
		}
	})
}

func TestIssueUploadTargetCreationIsObjectDrift(t *testing.T) {
	f := newTicketFixture(t)
	f.issueUploadSession(t, "upload-target-drift", "target.bin", (*f.clock).Add(time.Hour))
	s := f.service(t)
	ticket, err := s.IssueUpload(context.Background(), f.principal, "upload-target-drift", f.locator("target.bin"), 10)
	if err != nil {
		t.Fatalf("IssueUpload() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "target.bin"), []byte("raced"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationUpload); !errors.Is(err, ErrObjectDrift) {
		t.Fatalf("Verify(target creation) error = %v, want %v", err, ErrObjectDrift)
	}
}

func TestIssueUploadRejectsOwnerStatusAndLocatorMismatches(t *testing.T) {
	t.Run("owner mismatch", func(t *testing.T) {
		f := newTicketFixture(t)
		if _, err := f.db.Exec(`INSERT INTO accounts(id, email, display_name, role, status) VALUES ('acct-other', 'other@example.test', 'Other', 'member', 'active')`); err != nil {
			t.Fatalf("insert other account: %v", err)
		}
		f.issueUploadSession(t, "upload-owner", "owner.bin", (*f.clock).Add(time.Hour))
		if _, err := f.db.Exec(`UPDATE upload_sessions SET account_id = 'acct-other' WHERE id = 'upload-owner'`); err != nil {
			t.Fatalf("change upload owner: %v", err)
		}
		if _, err := f.service(t).IssueUpload(context.Background(), f.principal, "upload-owner", f.locator("owner.bin"), 10); !errors.Is(err, ErrUploadInvalid) {
			t.Fatalf("IssueUpload(owner mismatch) error = %v, want %v", err, ErrUploadInvalid)
		}
	})

	t.Run("status mismatch", func(t *testing.T) {
		f := newTicketFixture(t)
		f.issueUploadSession(t, "upload-status", "status.bin", (*f.clock).Add(time.Hour))
		if _, err := f.db.Exec(`UPDATE upload_sessions SET status = 'completed' WHERE id = 'upload-status'`); err != nil {
			t.Fatalf("close upload session: %v", err)
		}
		if _, err := f.service(t).IssueUpload(context.Background(), f.principal, "upload-status", f.locator("status.bin"), 10); !errors.Is(err, ErrUploadInvalid) {
			t.Fatalf("IssueUpload(status mismatch) error = %v, want %v", err, ErrUploadInvalid)
		}
	})

	t.Run("target mismatch", func(t *testing.T) {
		f := newTicketFixture(t)
		f.issueUploadSession(t, "upload-target", "target.bin", (*f.clock).Add(time.Hour))
		if _, err := f.service(t).IssueUpload(context.Background(), f.principal, "upload-target", f.locator("different.bin"), 10); !errors.Is(err, ErrUploadInvalid) {
			t.Fatalf("IssueUpload(target mismatch) error = %v, want %v", err, ErrUploadInvalid)
		}
	})

	t.Run("mount mismatch", func(t *testing.T) {
		f := newTicketFixture(t)
		otherRoot, err := os.MkdirTemp(".", ".ticket-other-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(otherRoot) })
		otherRoot, err = filepath.Abs(otherRoot)
		if err != nil {
			t.Fatal(err)
		}
		otherRoot, err = filepath.EvalSymlinks(otherRoot)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := mountid.Capture(otherRoot)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json) VALUES ('mount-other', 'Other', ?, 'common', 'external', 'normal', 'read_write', 'active', ?)`, otherRoot, string(encoded)); err != nil {
			t.Fatalf("insert other mount: %v", err)
		}
		f.issueUploadSession(t, "upload-mount", "mount.bin", (*f.clock).Add(time.Hour))
		if _, err := f.db.Exec(`UPDATE upload_sessions SET mount_id = 'mount-other' WHERE id = 'upload-mount'`); err != nil {
			t.Fatalf("change upload mount: %v", err)
		}
		if _, err := f.service(t).IssueUpload(context.Background(), f.principal, "upload-mount", f.locator("mount.bin"), 10); !errors.Is(err, ErrUploadInvalid) {
			t.Fatalf("IssueUpload(mount mismatch) error = %v, want %v", err, ErrUploadInvalid)
		}
	})
}

func TestVerifyRejectsUnderlyingUploadClosure(t *testing.T) {
	f := newTicketFixture(t)
	f.issueUploadSession(t, "upload-underlying-close", "underlying.bin", (*f.clock).Add(time.Hour))
	s := f.service(t)
	ticket, err := s.IssueUpload(context.Background(), f.principal, "upload-underlying-close", f.locator("underlying.bin"), 10)
	if err != nil {
		t.Fatalf("IssueUpload() error = %v", err)
	}
	if _, err := f.db.Exec(`UPDATE upload_sessions SET status = 'canceled', canceled_at = ? WHERE id = ?`, formatTime(*f.clock), "upload-underlying-close"); err != nil {
		t.Fatalf("cancel underlying upload: %v", err)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationUpload); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("Verify(underlying close) error = %v, want %v", err, ErrTicketClosed)
	}
}

func TestAddBytesIsAtomicUnderConcurrentReservations(t *testing.T) {
	f := newTicketFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "concurrent.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	s := f.service(t)
	ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("concurrent.txt"), 100)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	const workers = 20
	var wait sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := s.AddBytes(context.Background(), ticket.ID, 10); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, ErrByteBudgetExceeded) {
				t.Errorf("AddBytes() error = %v, want budget error after exhaustion", err)
			}
		}()
	}
	wait.Wait()
	if successes != 10 {
		t.Fatalf("successful AddBytes calls = %d, want 10", successes)
	}
	var consumed int64
	if err := f.db.QueryRow(`SELECT consumed_bytes FROM mcp_transfer_tickets WHERE id = ?`, ticket.ID).Scan(&consumed); err != nil {
		t.Fatalf("read consumed_bytes: %v", err)
	}
	if consumed != 100 {
		t.Fatalf("consumed_bytes = %d, want 100", consumed)
	}
}

func TestCloseIsOneShotUnderConcurrentRaces(t *testing.T) {
	f := newTicketFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "close-race.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	s := f.service(t)
	ticket, err := s.IssueDownload(context.Background(), f.principal, f.locator("close-race.txt"), 100)
	if err != nil {
		t.Fatalf("IssueDownload() error = %v", err)
	}
	const workers = 20
	var wait sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := s.Close(context.Background(), ticket.ID, StatusCanceled); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, ErrTicketClosed) {
				t.Errorf("Close() error = %v, want closed error after winner", err)
			}
		}()
	}
	wait.Wait()
	if successes != 1 {
		t.Fatalf("successful Close calls = %d, want 1", successes)
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM mcp_transfer_tickets WHERE id = ?`, ticket.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(StatusCanceled) {
		t.Fatalf("status = %q, want %q", status, StatusCanceled)
	}
}

func TestVerifyRejectsUploadClosureAndExpiredTicket(t *testing.T) {
	f := newTicketFixture(t)
	f.issueUploadSession(t, "upload-cancel", "cancel.bin", (*f.clock).Add(time.Hour))
	s := f.service(t, WithTTL(time.Minute))
	ticket, err := s.IssueUpload(context.Background(), f.principal, "upload-cancel", f.locator("cancel.bin"), 10)
	if err != nil {
		t.Fatalf("IssueUpload() error = %v", err)
	}
	if err := s.Close(context.Background(), ticket.ID, StatusCanceled); err != nil {
		t.Fatalf("Close(canceled) error = %v", err)
	}
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationUpload); !errors.Is(err, ErrTicketClosed) {
		t.Fatalf("Verify(canceled) error = %v, want closed", err)
	}

	if err := os.WriteFile(filepath.Join(f.root, "expire.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// Token revocation above is intentionally isolated to the upload ticket;
	// issue a fresh token for the expiry case.
	freshTokens := aitoken.NewService(f.db, aitoken.WithClock(func() time.Time { return *f.clock }))
	issued, err := freshTokens.Create(context.Background(), aitoken.CreateRequest{
		AccountID: f.account, Name: "expiry", Scopes: []aitoken.Scope{aitoken.ScopeFilesDownloadTicket},
		Boundaries: []aitoken.DirectoryBoundary{{Source: contentref.SourceCommonMount, MountID: f.mountID, RelativePath: "."}}, ExpiresAt: (*f.clock).Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create(expiry token) error = %v", err)
	}
	freshPrincipal := aitoken.Principal{AccountID: issued.Token.AccountID, TokenID: issued.Token.ID, PublicID: issued.Token.PublicID, Scopes: issued.Token.Scopes, Boundaries: issued.Token.Boundaries, ExpiresAt: issued.Token.ExpiresAt}
	ticket, err = s.IssueDownload(context.Background(), freshPrincipal, f.locator("expire.txt"), 1)
	if err != nil {
		t.Fatalf("IssueDownload(expire) error = %v", err)
	}
	*f.clock = (*f.clock).Add(2 * time.Minute)
	if _, err := s.Verify(context.Background(), ticket.BearerToken, OperationDownload); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("Verify(expired) error = %v, want expired", err)
	}
}
