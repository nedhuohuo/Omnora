package confirmation

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/store"
)

type fixture struct {
	db        *sql.DB
	principal aitoken.Principal
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dbHandle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "confirmation.db"),
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = dbHandle.Close() })
	db := dbHandle.SQL()
	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-confirm', 'confirm@example.test', 'Confirm', 'member', 'active')
`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json)
VALUES ('mount-confirm', 'Confirm', '/tmp/confirm', 'common', 'external', 'normal', 'read_write', 'active', '{}');
INSERT INTO mount_grants(mount_id, account_id, permission)
VALUES ('mount-confirm', 'acct-confirm', 'editor')
`); err != nil {
		t.Fatalf("insert mount grant: %v", err)
	}
	issued, err := aitoken.NewService(db).Create(context.Background(), aitoken.CreateRequest{
		AccountID: "acct-confirm",
		Name:      "confirmation test",
		Scopes:    []aitoken.Scope{aitoken.ScopeFilesPurge},
		Boundaries: []aitoken.DirectoryBoundary{{
			Source:       contentref.SourceCommonMount,
			MountID:      "mount-confirm",
			RelativePath: ".",
		}},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() token error = %v", err)
	}
	return fixture{db: db, principal: aitoken.Principal{
		AccountID:  issued.Token.AccountID,
		TokenID:    issued.Token.ID,
		PublicID:   issued.Token.PublicID,
		Scopes:     issued.Token.Scopes,
		Boundaries: issued.Token.Boundaries,
		ExpiresAt:  issued.Token.ExpiresAt,
	}}
}

func TestBeginStoresHashOnlyAndBindsCanonicalArguments(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	service := NewService(f.db, WithClock(func() time.Time { return now }))
	preview := Preview{
		ToolName:          "files.delete_permanently",
		Args:              []byte(`{"b":2,"a":{"d":4,"c":3}}`),
		ObjectFingerprint: "fp-1",
		Impact: Impact{
			Summary:   "Delete one file",
			ItemCount: 1,
			TotalSize: 42,
			Details:   map[string]any{"label": "docs/report.txt"},
		},
	}
	challenge, err := service.Begin(context.Background(), f.principal, preview)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if challenge.ExpiresAt != now.Add(defaultTTL) {
		t.Fatalf("ExpiresAt = %s, want %s", challenge.ExpiresAt, now.Add(defaultTTL))
	}
	parts := strings.Split(challenge.RequestState, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("RequestState = %q, want publicID.secret", challenge.RequestState)
	}
	var storedPublicID, secretHash, argsHash, status string
	var expiresAt string
	if err := f.db.QueryRow(`
SELECT public_id, secret_hash, args_hash, status, expires_at
FROM mcp_confirmations WHERE public_id = ?
`, parts[0]).Scan(&storedPublicID, &secretHash, &argsHash, &status, &expiresAt); err != nil {
		t.Fatalf("query confirmation: %v", err)
	}
	if storedPublicID != parts[0] || status != "pending" {
		t.Fatalf("stored publicID/status = %q/%q", storedPublicID, status)
	}
	if secretHash == parts[1] || strings.Contains(secretHash, parts[1]) || !strings.HasPrefix(secretHash, "sha256:") {
		t.Fatalf("secret hash = %q, plaintext secret leaked", secretHash)
	}
	if expiresAt == "" || argsHash == "" {
		t.Fatalf("stored expiry/args hash = %q/%q", expiresAt, argsHash)
	}

	// Key order and insignificant whitespace must not change the binding.
	err = service.Consume(context.Background(), f.principal, ConsumeRequest{
		RequestState:      challenge.RequestState,
		ToolName:          preview.ToolName,
		Args:              []byte(` { "a": { "c": 3, "d": 4 }, "b": 2 } `),
		ObjectFingerprint: preview.ObjectFingerprint,
		Decision:          "accept",
	})
	if err != nil {
		t.Fatalf("Consume() canonical equivalent error = %v", err)
	}
	assertStatus(t, f.db, parts[0], "accepted")
}

func TestConsumeBindingsFailClosedWithoutConsumingChallenge(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	service := NewService(f.db, WithClock(func() time.Time { return now }))
	preview := Preview{ToolName: "files.move", Args: []byte(`{"path":"a.txt"}`), ObjectFingerprint: "fp-1"}
	challenge, err := service.Begin(context.Background(), f.principal, preview)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mutations := []struct {
		name string
		fn   func(ConsumeRequest, aitoken.Principal) (ConsumeRequest, aitoken.Principal)
	}{
		{name: "wrong account", fn: func(req ConsumeRequest, p aitoken.Principal) (ConsumeRequest, aitoken.Principal) {
			p.AccountID = "acct-other"
			return req, p
		}},
		{name: "wrong token", fn: func(req ConsumeRequest, p aitoken.Principal) (ConsumeRequest, aitoken.Principal) {
			p.TokenID = "aitok-other"
			return req, p
		}},
		{name: "wrong tool", fn: func(req ConsumeRequest, p aitoken.Principal) (ConsumeRequest, aitoken.Principal) {
			req.ToolName = "files.trash"
			return req, p
		}},
		{name: "wrong object", fn: func(req ConsumeRequest, p aitoken.Principal) (ConsumeRequest, aitoken.Principal) {
			req.ObjectFingerprint = "fp-other"
			return req, p
		}},
		{name: "wrong args", fn: func(req ConsumeRequest, p aitoken.Principal) (ConsumeRequest, aitoken.Principal) {
			req.Args = []byte(`{"path":"b.txt"}`)
			return req, p
		}},
		{name: "wrong secret", fn: func(req ConsumeRequest, p aitoken.Principal) (ConsumeRequest, aitoken.Principal) {
			req.RequestState = strings.Split(req.RequestState, ".")[0] + ".wrong-secret"
			return req, p
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			req := ConsumeRequest{
				RequestState:      challenge.RequestState,
				ToolName:          preview.ToolName,
				Args:              preview.Args,
				ObjectFingerprint: preview.ObjectFingerprint,
				Decision:          "accept",
			}
			req, principal := mutation.fn(req, f.principal)
			if err := service.Consume(context.Background(), principal, req); err == nil {
				t.Fatal("Consume() unexpectedly succeeded for binding drift")
			}
			assertStatus(t, f.db, strings.Split(challenge.RequestState, ".")[0], "pending")
		})
	}
}

func TestConsumeDeclineExpiryMalformedAndConfirmArgument(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	clock := now
	service := NewService(f.db, WithClock(func() time.Time { return clock }))
	preview := Preview{ToolName: "files.trash", Args: []byte(`{"confirm":true,"path":"a.txt"}`), ObjectFingerprint: "fp-1"}
	challenge, err := service.Begin(context.Background(), f.principal, preview)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	base := ConsumeRequest{RequestState: challenge.RequestState, ToolName: preview.ToolName, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint}
	if err := service.Consume(context.Background(), f.principal, base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("confirm:true without explicit decision error = %v, want ErrInvalidInput", err)
	}
	if err := service.Consume(context.Background(), f.principal, ConsumeRequest{RequestState: "malformed", ToolName: preview.ToolName, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "accept"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed state error = %v, want ErrInvalidInput", err)
	}
	if err := service.Consume(context.Background(), f.principal, ConsumeRequest{RequestState: challenge.RequestState, ToolName: preview.ToolName, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "decline"}); !errors.Is(err, ErrDeclined) {
		t.Fatalf("decline error = %v, want ErrDeclined", err)
	}
	if err := service.Consume(context.Background(), f.principal, baseWithDecision(base, "accept")); !errors.Is(err, ErrConsumed) {
		t.Fatalf("repeated consume error = %v, want ErrConsumed", err)
	}
	assertStatus(t, f.db, strings.Split(challenge.RequestState, ".")[0], "declined")

	expired, err := service.Begin(context.Background(), f.principal, Preview{ToolName: "files.purge", Args: []byte(`{}`), ObjectFingerprint: "fp-2"})
	if err != nil {
		t.Fatalf("Begin(expired) error = %v", err)
	}
	clock = now.Add(defaultTTL)
	err = service.Consume(context.Background(), f.principal, ConsumeRequest{RequestState: expired.RequestState, ToolName: "files.purge", Args: []byte(`{}`), ObjectFingerprint: "fp-2", Decision: "accept"})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired consume error = %v, want ErrExpired", err)
	}
	assertStatus(t, f.db, strings.Split(expired.RequestState, ".")[0], "expired")
}

func TestConsumeConcurrentConsumersExactlyOneSucceeds(t *testing.T) {
	f := newFixture(t)
	f.db.SetMaxOpenConns(8)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	service := NewService(f.db, WithClock(func() time.Time { return now }))
	preview := Preview{ToolName: "files.delete_permanently", Args: []byte(`{"path":"a.txt"}`), ObjectFingerprint: "fp-race"}
	challenge, err := service.Begin(context.Background(), f.principal, preview)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	req := ConsumeRequest{RequestState: challenge.RequestState, ToolName: preview.ToolName, Args: preview.Args, ObjectFingerprint: preview.ObjectFingerprint, Decision: "accept"}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- service.Consume(context.Background(), f.principal, req)
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successful consumes = %d, want exactly 1", successes)
	}
	assertStatus(t, f.db, strings.Split(challenge.RequestState, ".")[0], "accepted")
}

func baseWithDecision(req ConsumeRequest, decision string) ConsumeRequest {
	req.Decision = decision
	return req
}

func assertStatus(t *testing.T, db *sql.DB, publicID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow("SELECT status FROM mcp_confirmations WHERE public_id = ?", publicID).Scan(&got); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if got != want {
		t.Fatalf("confirmation status = %q, want %q", got, want)
	}
}

func TestErrorsDoNotIncludeSecretOrHash(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	service := NewService(f.db, WithClock(func() time.Time { return now }))
	challenge, err := service.Begin(context.Background(), f.principal, Preview{ToolName: "files.move", Args: []byte(`{}`), ObjectFingerprint: "fp"})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	secret := strings.Split(challenge.RequestState, ".")[1]
	err = service.Consume(context.Background(), f.principal, ConsumeRequest{RequestState: strings.Split(challenge.RequestState, ".")[0] + ".wrong", ToolName: "files.move", Args: []byte(`{}`), ObjectFingerprint: "fp", Decision: "accept"})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sha256:") {
		t.Fatalf("error = %v, secret/hash leaked", err)
	}
}
