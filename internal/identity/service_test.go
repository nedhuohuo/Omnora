package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"omnora/internal/domain"
	"omnora/internal/personalstorage"

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
	totp_secret_ciphertext TEXT,
	totp_confirmed_at TEXT,
	totp_pending_secret_ciphertext TEXT,
	totp_pending_expires_at TEXT,
	password_reset_required INTEGER NOT NULL DEFAULT 0 CHECK (password_reset_required IN (0, 1)),
	totp_reset_required INTEGER NOT NULL DEFAULT 0 CHECK (totp_reset_required IN (0, 1)),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE system_state (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO system_state(key, value) VALUES ('credential_generation', '7');

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

CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL UNIQUE,
	root_path TEXT NOT NULL UNIQUE,
	purpose TEXT NOT NULL CHECK (purpose IN ('personal_default', 'common')),
	storage_kind TEXT NOT NULL CHECK (storage_kind IN ('managed', 'external')),
	governance TEXT NOT NULL CHECK (governance IN ('system', 'normal', 'restricted')),
	mode TEXT NOT NULL CHECK (mode IN ('read_only', 'read_write')),
	index_enabled INTEGER NOT NULL DEFAULT 0 CHECK (index_enabled IN (0, 1)),
	status TEXT NOT NULL,
	mount_identity_json TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX mounts_single_personal_default_idx
	ON mounts(purpose) WHERE purpose = 'personal_default';

CREATE TABLE personal_directories (
	account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
	relative_path TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL CHECK (state IN ('ready', 'retained')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

func TestInitializeConsumesTokenOnceAndCreatesAdminPersonalDirectory(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTargetTestService(t, db)

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
	if created.PersonalDirectory.AccountID != created.Account.ID || created.PersonalDirectory.RelativePath != created.Account.ID || created.PersonalDirectory.State != "ready" {
		t.Fatalf("personal directory not tied to account: %#v", created.PersonalDirectory)
	}

	var state string
	err = db.QueryRowContext(ctx, `
SELECT state
FROM personal_directories
WHERE account_id = ? AND relative_path = ?
`, created.Account.ID, created.Account.ID).Scan(&state)
	if err != nil {
		t.Fatalf("query personal directory: %v", err)
	}
	if state != "ready" {
		t.Fatalf("state = %q, want ready", state)
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
	svc := newTargetTestService(t, db)

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
	svc := newTargetTestService(t, db)

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
	svc := newTargetTestService(t, db)

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
		Purpose:   SessionPurposeFull,
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

func TestCreateSessionPersistsFullPurposeAndCredentialGeneration(t *testing.T) {
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
		Purpose:   SessionPurposeFull,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	var purpose string
	var generation int64
	if err := db.QueryRowContext(ctx, `
SELECT purpose, credential_generation
FROM identity_sessions
WHERE id = ?
`, issued.Session.ID).Scan(&purpose, &generation); err != nil {
		t.Fatalf("query session security fields: %v", err)
	}
	if purpose != string(SessionPurposeFull) {
		t.Fatalf("purpose = %q, want %q", purpose, SessionPurposeFull)
	}
	if generation != 7 {
		t.Fatalf("credential_generation = %d, want 7", generation)
	}
}

func TestVerifySessionRejectsStaleGenerationAndUnknownPurpose(t *testing.T) {
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
		Purpose:   SessionPurposeFull,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE system_state SET value = '8', updated_at = CURRENT_TIMESTAMP
WHERE key = 'credential_generation'
`); err != nil {
		t.Fatalf("bump credential generation: %v", err)
	}
	if _, err := svc.VerifySession(ctx, issued.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("VerifySession() after generation bump error = %v, want ErrSessionInvalid", err)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE system_state SET value = '7', updated_at = CURRENT_TIMESTAMP
WHERE key = 'credential_generation'
`); err != nil {
		t.Fatalf("restore credential generation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE identity_sessions SET purpose = 'legacy' WHERE id = ?
`, issued.Session.ID); err != nil {
		t.Fatalf("set unknown purpose: %v", err)
	}
	if _, err := svc.VerifySession(ctx, issued.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("VerifySession() with unknown purpose error = %v, want ErrSessionInvalid", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE identity_sessions SET purpose = NULL WHERE id = ?`, issued.Session.ID); err != nil {
		t.Fatalf("clear purpose: %v", err)
	}
	if _, err := svc.VerifySession(ctx, issued.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("VerifySession() with null purpose error = %v, want ErrSessionInvalid", err)
	}
}

func TestVerifyAndListSessionsScanReauthenticatedAt(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	created, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "reauth@example.com", DisplayName: "Reauth", Password: "CorrectHorse1!"})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	issued, err := svc.CreateSession(ctx, SessionRequest{AccountID: created.Account.ID, TTL: time.Hour, Purpose: SessionPurposeFull})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	const reauthenticatedAt = "2026-08-06T12:00:00Z"
	if _, err := db.ExecContext(ctx, `UPDATE identity_sessions SET reauthenticated_at = ? WHERE id = ?`, reauthenticatedAt, issued.Session.ID); err != nil {
		t.Fatalf("set reauthenticated_at: %v", err)
	}
	verified, err := svc.VerifySession(ctx, issued.Token)
	if err != nil {
		t.Fatalf("VerifySession() error = %v", err)
	}
	if verified.ReauthenticatedAt.IsZero() || verified.ReauthenticatedAt.Format(time.RFC3339) != reauthenticatedAt {
		t.Fatalf("verified reauthenticated_at = %v, want %s", verified.ReauthenticatedAt, reauthenticatedAt)
	}
	sessions, err := svc.ListSessions(ctx, created.Account.ID)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ReauthenticatedAt.IsZero() {
		t.Fatalf("listed sessions = %#v, want reauthenticated_at", sessions)
	}
}

func TestListSessionsPreservesPurposeAndCredentialGeneration(t *testing.T) {
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
	if _, err := svc.CreateSession(ctx, SessionRequest{
		AccountID: created.Account.ID,
		TTL:       time.Hour,
		Purpose:   SessionPurposeFull,
	}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	sessions, err := svc.ListSessions(ctx, created.Account.ID)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() count = %d, want 1", len(sessions))
	}
	if sessions[0].Purpose != SessionPurposeFull {
		t.Fatalf("listed purpose = %q, want %q", sessions[0].Purpose, SessionPurposeFull)
	}
	if sessions[0].CredentialGeneration != 7 {
		t.Fatalf("listed credential_generation = %d, want 7", sessions[0].CredentialGeneration)
	}
}

func TestCreateSessionRequiresExplicitPurpose(t *testing.T) {
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

	if _, err := svc.CreateSession(ctx, SessionRequest{AccountID: created.Account.ID, TTL: time.Hour}); err == nil {
		t.Fatal("CreateSession() with empty purpose succeeded")
	}
	issued, err := svc.CreateSession(ctx, SessionRequest{AccountID: created.Account.ID, TTL: time.Hour, Purpose: SessionPurposeFull})
	if err != nil {
		t.Fatalf("CreateSession() full error = %v", err)
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

func TestCreateSessionRejectsEnrollmentForMember(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	created, err := svc.CreateAccount(ctx, CreateAccountRequest{Email: "member-enrollment@example.com", DisplayName: "Member", Password: "CorrectHorse1!"})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := svc.CreateSession(ctx, SessionRequest{AccountID: created.Account.ID, TTL: time.Hour, Purpose: SessionPurposeTOTPEnrollment}); err == nil {
		t.Fatal("member enrollment session creation succeeded")
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
		Purpose:   SessionPurposeFull,
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

	db, err := sql.Open("sqlite", ":memory:")
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
	if _, err := db.Exec(`
ALTER TABLE identity_sessions ADD COLUMN purpose TEXT;
ALTER TABLE identity_sessions ADD COLUMN reauthenticated_at TEXT;
ALTER TABLE identity_sessions ADD COLUMN credential_generation INTEGER;
`); err != nil {
		t.Fatalf("install identity security columns: %v", err)
	}
	return db
}

func newTestService(db *sql.DB) *Service {
	managedDir, err := os.MkdirTemp("", "omnora-identity-test-")
	if err != nil {
		panic(err)
	}
	managedDir, err = filepath.EvalSymlinks(managedDir)
	if err != nil {
		panic(err)
	}
	return New(db, Options{
		Clock: func() time.Time {
			return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
		},
		PasswordIterations: 2,
		ManagedDir:         managedDir,
	})
}

func newTargetTestService(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	svc := newTestService(db)
	managedDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.personalStorage = personalstorage.New(db, managedDir)
	return svc
}
