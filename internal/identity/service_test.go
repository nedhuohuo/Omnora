package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"omnora/internal/domain"

	_ "modernc.org/sqlite"
)

const coreTestSchema = `
CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
	password_hash TEXT,
	totp_required INTEGER NOT NULL DEFAULT 0 CHECK (totp_required IN (0, 1)),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE spaces (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN ('personal', 'shared')),
	name TEXT NOT NULL,
	owner_account_id TEXT REFERENCES accounts(id),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE space_members (
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor', 'manager')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (space_id, account_id)
);
`

func TestInitializeConsumesTokenOnceAndCreatesAdminSpace(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	secret, err := svc.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PrepareInitialization() error = %v", err)
	}
	if secret.Token == "" || secret.TokenHash == "" || secret.Token == secret.TokenHash {
		t.Fatalf("initialization secret should expose token once and store only hash: %#v", secret)
	}

	created, err := svc.Initialize(ctx, InitializationRequest{
		Token:       secret.Token,
		Email:       " Admin@Example.COM ",
		DisplayName: "Ada Admin",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if created.Account.Role != domain.AccountRoleAdmin {
		t.Fatalf("role = %q, want admin", created.Account.Role)
	}
	if created.Account.Email != "admin@example.com" {
		t.Fatalf("email = %q, want normalized email", created.Account.Email)
	}
	if created.Account.PasswordHash == "CorrectHorse1!" {
		t.Fatalf("stored account contains plaintext password")
	}
	if !svc.VerifyPassword("CorrectHorse1!", created.Account.PasswordHash) {
		t.Fatalf("stored password hash did not verify")
	}
	if created.PersonalSpace.Kind != "personal" || created.PersonalSpace.OwnerAccountID != created.Account.ID {
		t.Fatalf("personal space not tied to account: %#v", created.PersonalSpace)
	}
	if created.PersonalSpace.Name != "My Space" {
		t.Fatalf("personal space name = %q, want My Space", created.PersonalSpace.Name)
	}

	var permission string
	err = db.QueryRowContext(ctx, `
SELECT permission
FROM space_members
WHERE account_id = ? AND space_id = ?
`, created.Account.ID, created.PersonalSpace.ID).Scan(&permission)
	if err != nil {
		t.Fatalf("query space membership: %v", err)
	}
	if permission != string(domain.SpacePermissionManager) {
		t.Fatalf("permission = %q, want manager", permission)
	}

	_, err = svc.Initialize(ctx, InitializationRequest{
		Token:       secret.Token,
		Email:       "other@example.com",
		DisplayName: "Other Admin",
		Password:    "CorrectHorse1!",
	})
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second Initialize() error = %v, want ErrAlreadyInitialized", err)
	}
}

func TestInitializeWrongTokenDoesNotConsume(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	secret, err := svc.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PrepareInitialization() error = %v", err)
	}
	_, err = svc.Initialize(ctx, InitializationRequest{
		Token:       "wrong-token",
		Email:       "admin@example.com",
		DisplayName: "Ada Admin",
		Password:    "CorrectHorse1!",
	})
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong-token Initialize() error = %v, want ErrInvalidCredential", err)
	}

	_, err = svc.Initialize(ctx, InitializationRequest{
		Token:       secret.Token,
		Email:       "admin@example.com",
		DisplayName: "Ada Admin",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("Initialize() after wrong token error = %v", err)
	}
}

func TestConcurrentInitializeAllowsAtMostOneSuccess(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	secret, err := svc.PrepareInitialization(ctx, time.Hour)
	if err != nil {
		t.Fatalf("PrepareInitialization() error = %v", err)
	}

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := svc.Initialize(ctx, InitializationRequest{
				Token:       secret.Token,
				Email:       fmt.Sprintf("admin-%d@example.com", i),
				DisplayName: "Ada Admin",
				Password:    "CorrectHorse1!",
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful Initialize() calls = %d, want 1", successes)
	}

	var accounts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM accounts").Scan(&accounts); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if accounts != 1 {
		t.Fatalf("accounts = %d, want 1", accounts)
	}
}

func TestCreateAccountValidationAndDuplicateEmail(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	_, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "not an email",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid email error = %v, want ErrInvalidInput", err)
	}

	_, err = svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "short",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("weak password error = %v, want ErrInvalidInput", err)
	}

	first, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if first.Account.Role != domain.AccountRoleMember {
		t.Fatalf("default role = %q, want member", first.Account.Role)
	}
	authenticated, err := svc.Authenticate(ctx, " MEMBER@example.com ", "CorrectHorse1!")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if authenticated.ID != first.Account.ID {
		t.Fatalf("authenticated account ID = %q, want %q", authenticated.ID, first.Account.ID)
	}
	_, err = svc.Authenticate(ctx, "member@example.com", "WrongHorse1!")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("Authenticate() wrong password error = %v, want ErrInvalidCredential", err)
	}

	_, err = svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "MEMBER@example.com",
		DisplayName: "Member Two",
		Password:    "CorrectHorse1!",
	})
	if !errors.Is(err, ErrAccountExists) {
		t.Fatalf("duplicate CreateAccount() error = %v, want ErrAccountExists", err)
	}
}

func TestSessionTokenIsOpaqueAndStoredHashed(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	created, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	issued, err := svc.CreateSession(ctx, SessionRequest{
		AccountID: created.Account.ID,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if issued.Token == "" || strings.Contains(issued.Session.TokenHash, issued.Token) {
		t.Fatalf("session token should be opaque and stored as hash: %#v", issued)
	}

	var storedHash string
	if err := db.QueryRowContext(ctx, "SELECT token_hash FROM identity_sessions WHERE id = ?", issued.Session.ID).Scan(&storedHash); err != nil {
		t.Fatalf("query stored session hash: %v", err)
	}
	if storedHash == issued.Token || !strings.HasPrefix(storedHash, "sha256:") {
		t.Fatalf("stored session token = %q, want sha256 hash only", storedHash)
	}

	verified, err := svc.VerifySession(ctx, issued.Token)
	if err != nil {
		t.Fatalf("VerifySession() error = %v", err)
	}
	if verified.ID != issued.Session.ID || verified.TokenHash != storedHash {
		t.Fatalf("verified session mismatch: %#v", verified)
	}

	if err := svc.RevokeSession(ctx, issued.Token); err != nil {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	_, err = svc.VerifySession(ctx, issued.Token)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("VerifySession() after revoke error = %v, want ErrSessionInvalid", err)
	}
}

func TestSessionEntryDefaultAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	created, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	// An empty entry is normalized to the default HTTP entry so sessions
	// created without an explicit entry remain compatible with the schema.
	issued, err := svc.CreateSession(ctx, SessionRequest{AccountID: created.Account.ID, TTL: time.Hour})
	if err != nil {
		t.Fatalf("CreateSession() default error = %v", err)
	}
	if issued.Session.Entry != DefaultSessionEntry {
		t.Fatalf("default session entry = %q, want %q", issued.Session.Entry, DefaultSessionEntry)
	}
	verified, err := svc.VerifySession(ctx, issued.Token)
	if err != nil {
		t.Fatalf("VerifySession() error = %v", err)
	}
	if verified.Entry != DefaultSessionEntry {
		t.Fatalf("verified entry = %q, want %q", verified.Entry, DefaultSessionEntry)
	}
}

func TestDisabledAccountInvalidatesSession(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	created, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	issued, err := svc.CreateSession(ctx, SessionRequest{
		AccountID: created.Account.ID,
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE accounts SET status = 'disabled' WHERE id = ?", created.Account.ID); err != nil {
		t.Fatalf("disable account: %v", err)
	}

	_, err = svc.VerifySession(ctx, issued.Token)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("VerifySession() disabled account error = %v, want ErrSessionInvalid", err)
	}
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:identity-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close sqlite: %v", err)
		}
	})
	if _, err := db.Exec(coreTestSchema); err != nil {
		t.Fatalf("install core schema: %v", err)
	}
	if err := InstallSchema(context.Background(), db); err != nil {
		t.Fatalf("install identity schema: %v", err)
	}
	return db
}

func newTestService(db *sql.DB) *Service {
	return New(db, Options{
		Clock: func() time.Time {
			return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
		},
		PasswordIterations: 2,
	})
}
