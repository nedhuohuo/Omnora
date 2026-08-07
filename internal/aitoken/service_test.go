package aitoken

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const coreSchema = `
CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE spaces (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN ('personal', 'shared')),
	name TEXT NOT NULL,
	owner_account_id TEXT REFERENCES accounts(id),
	status TEXT NOT NULL DEFAULT 'active'
);

CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	space_id TEXT NOT NULL REFERENCES spaces(id),
	display_name TEXT NOT NULL,
	root_path TEXT NOT NULL,
	kind TEXT NOT NULL,
	mode TEXT NOT NULL,
	status TEXT NOT NULL
);

CREATE TABLE system_state (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO system_state(key, value) VALUES ('credential_generation', '7');
`

func TestCreateHashesSecretAndVerifyBearerReturnsPrincipal(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))

	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1",
		Name:      "Local editor MCP",
		Scopes:    []Scope{ScopeSpacesRead, ScopeFilesList, ScopeFilesMetadata},
		Boundaries: []DirectoryBoundary{{
			SpaceID:      "space_1",
			MountID:      "mount_1",
			RelativePath: "docs/./notes",
		}},
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.Secret == "" || issued.BearerToken != issued.Token.PublicID+"."+issued.Secret {
		t.Fatalf("issued token did not include one-time bearer material: %#v", issued)
	}
	if issued.Token.SecretHash == issued.Secret || !strings.HasPrefix(issued.Token.SecretHash, "sha256:") {
		t.Fatalf("SecretHash = %q, want hash only", issued.Token.SecretHash)
	}
	if issued.Token.Boundaries[0].RelativePath != "docs/notes" {
		t.Fatalf("boundary path = %q, want normalized docs/notes", issued.Token.Boundaries[0].RelativePath)
	}

	var storedHash string
	if err := db.QueryRowContext(ctx, "SELECT secret_hash FROM ai_tokens WHERE public_id = ?", issued.Token.PublicID).Scan(&storedHash); err != nil {
		t.Fatalf("query secret_hash: %v", err)
	}
	if storedHash == issued.Secret || !secretMatches(issued.Secret, storedHash) {
		t.Fatal("stored secret hash does not verify returned secret")
	}

	principal, err := service.VerifyBearer(ctx, "Bearer "+issued.BearerToken)
	if err != nil {
		t.Fatalf("VerifyBearer() error = %v", err)
	}
	if principal.AccountID != "acct_1" || principal.TokenID != issued.Token.ID || !principal.HasScope(ScopeFilesList) {
		t.Fatalf("principal metadata mismatch: %#v", principal)
	}
	if len(principal.Boundaries) != 1 || principal.Boundaries[0].RelativePath != "docs/notes" {
		t.Fatalf("principal boundaries = %#v, want normalized boundary", principal.Boundaries)
	}

	var lastUsedAt string
	if err := db.QueryRowContext(ctx, "SELECT last_used_at FROM ai_tokens WHERE id = ?", issued.Token.ID).Scan(&lastUsedAt); err != nil {
		t.Fatalf("query last_used_at: %v", err)
	}
	if lastUsedAt != formatTime(now) {
		t.Fatalf("last_used_at = %q, want %q", lastUsedAt, formatTime(now))
	}
}

func TestAITokenCredentialGenerationIsStoredAndEnforced(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))

	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1",
		Name:      "epoch test",
		Scopes:    []Scope{ScopeSpacesRead},
		Boundaries: []DirectoryBoundary{{
			SpaceID: "space_1", MountID: "mount_1", RelativePath: ".",
		}},
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var generation int64
	if err := db.QueryRowContext(ctx, "SELECT credential_generation FROM ai_tokens WHERE id = ?", issued.Token.ID).Scan(&generation); err != nil {
		t.Fatalf("query credential_generation: %v", err)
	}
	if generation != 7 {
		t.Fatalf("credential_generation = %d, want 7", generation)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE system_state SET value = '8', updated_at = CURRENT_TIMESTAMP
WHERE key = 'credential_generation'
`); err != nil {
		t.Fatalf("bump credential generation: %v", err)
	}
	if _, err := service.VerifyBearer(ctx, "Bearer "+issued.BearerToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyBearer() after generation bump error = %v, want ErrInvalidToken", err)
	}
}

func TestCreateWithoutExpiryNeverExpires(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))

	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1",
		Name:      "never-expiring MCP",
		Scopes:    []Scope{ScopeSpacesRead},
		Boundaries: []DirectoryBoundary{{
			SpaceID: "space_1", MountID: "mount_1", RelativePath: ".",
		}},
	})
	if err != nil {
		t.Fatalf("Create() without expiry error = %v", err)
	}
	if !issued.Token.ExpiresAt.IsZero() {
		t.Fatalf("issued token ExpiresAt = %v, want zero value", issued.Token.ExpiresAt)
	}
	var storedExpiry string
	if err := db.QueryRowContext(ctx, "SELECT expires_at FROM ai_tokens WHERE id = ?", issued.Token.ID).Scan(&storedExpiry); err != nil {
		t.Fatalf("query expires_at: %v", err)
	}
	if storedExpiry != "" {
		t.Fatalf("stored expires_at = %q, want empty string", storedExpiry)
	}

	// The token must remain valid well beyond the original creation time.
	farFuture := NewService(db, WithClock(func() time.Time { return now.Add(100 * 365 * 24 * time.Hour) }))
	principal, err := farFuture.VerifyBearer(ctx, issued.BearerToken)
	if err != nil {
		t.Fatalf("VerifyBearer() far in the future error = %v, want success", err)
	}
	if !principal.ExpiresAt.IsZero() {
		t.Fatalf("principal ExpiresAt = %v, want zero value", principal.ExpiresAt)
	}
	if _, err := farFuture.RefreshPrincipal(ctx, issued.Token.ID); err != nil {
		t.Fatalf("RefreshPrincipal() far in the future error = %v, want success", err)
	}
}

func TestValidateScopesRejectsUnknownDuplicateAndEmpty(t *testing.T) {
	for _, scopes := range [][]Scope{
		nil,
		{},
		{ScopeFilesList, ScopeFilesList},
		{Scope("admin:delete")},
	} {
		if _, err := ValidateScopes(scopes); !errors.Is(err, ErrInvalidScope) {
			t.Fatalf("ValidateScopes(%v) error = %v, want ErrInvalidScope", scopes, err)
		}
	}
	if got, err := ValidateScopes([]Scope{ScopeSearchRead, ScopeUploadsCreate}); err != nil || len(got) != 2 {
		t.Fatalf("ValidateScopes(valid) = %v, %v", got, err)
	}
	expected := []Scope{
		"spaces:read",
		"files:list",
		"files:metadata",
		"files:text",
		"files:download_ticket",
		"search:read",
		"uploads:create",
		"files:write",
		"files:trash",
		"trash:read",
		"files:restore",
		"files:purge",
		"shares:read",
		"shares:create",
		"shares:revoke",
	}
	if got := AllowlistedScopes(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("AllowlistedScopes() = %v, want %v", got, expected)
	}
	if got, err := ValidateScopes(expected); err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("ValidateScopes(expected) = %v, %v; want %v", got, err, expected)
	}
}

func TestRefreshPrincipalReloadsCurrentTokenAndRejectsInactiveStates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1",
		Name:      "transfer client",
		Scopes:    []Scope{ScopeFilesWrite},
		Boundaries: []DirectoryBoundary{{
			SpaceID: "space_1", MountID: "mount_1", RelativePath: "docs",
		}},
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ai_tokens SET scopes = ? WHERE id = ?`, `["files:trash"]`, issued.Token.ID); err != nil {
		t.Fatalf("update token scopes: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM ai_token_boundaries WHERE token_id = ?`, issued.Token.ID); err != nil {
		t.Fatalf("replace token boundaries: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO ai_token_boundaries(token_id, space_id, mount_id, relative_path)
VALUES (?, 'space_1', 'mount_1', 'refreshed')
`, issued.Token.ID); err != nil {
		t.Fatalf("insert refreshed token boundary: %v", err)
	}
	principal, err := service.RefreshPrincipal(ctx, issued.Token.ID)
	if err != nil {
		t.Fatalf("RefreshPrincipal() error = %v", err)
	}
	if principal.TokenID != issued.Token.ID || !principal.HasScope(ScopeFilesTrash) || principal.HasScope(ScopeFilesWrite) {
		t.Fatalf("refreshed principal = %#v", principal)
	}
	if len(principal.Boundaries) != 1 || principal.Boundaries[0].SpaceID != "space_1" ||
		principal.Boundaries[0].MountID != "mount_1" || principal.Boundaries[0].RelativePath != "refreshed" {
		t.Fatalf("refreshed principal boundaries = %#v", principal.Boundaries)
	}

	for _, tc := range []struct {
		name  string
		stmt  string
		args  []any
		clock time.Time
	}{
		{name: "inactive account", stmt: `UPDATE accounts SET status = 'disabled' WHERE id = 'acct_1'`, clock: now},
		{name: "revoked token", stmt: `UPDATE ai_tokens SET revoked_at = ? WHERE id = ?`, args: []any{formatTime(now), issued.Token.ID}, clock: now},
		{name: "expired token", stmt: `UPDATE ai_tokens SET expires_at = ? WHERE id = ?`, args: []any{formatTime(now), issued.Token.ID}, clock: now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testDB := cloneTokenDB(t, db)
			if tc.name == "inactive account" {
				if _, err := testDB.ExecContext(ctx, tc.stmt, tc.args...); err != nil {
					t.Fatalf("setup: %v", err)
				}
			} else {
				if _, err := testDB.ExecContext(ctx, `UPDATE accounts SET status = 'active' WHERE id = 'acct_1'`); err != nil {
					t.Fatalf("activate account: %v", err)
				}
				if _, err := testDB.ExecContext(ctx, tc.stmt, tc.args...); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			refreshService := NewService(testDB, WithClock(func() time.Time { return tc.clock }))
			if _, err := refreshService.RefreshPrincipal(ctx, issued.Token.ID); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("RefreshPrincipal() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyBearerRejectsInvalidCredentialStates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1",
		Name:      "REST client",
		Scopes:    []Scope{ScopeFilesText},
		Boundaries: []DirectoryBoundary{{
			SpaceID: "space_1",
			MountID: "mount_1",
		}},
		ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
		token string
	}{
		{
			name:  "wrong secret",
			token: issued.Token.PublicID + ".wrong",
		},
		{
			name: "revoked",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, "UPDATE ai_tokens SET revoked_at = ? WHERE id = ?", formatTime(now), issued.Token.ID); err != nil {
					t.Fatalf("revoke token: %v", err)
				}
			},
			token: issued.BearerToken,
		},
		{
			name: "account disabled",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, "UPDATE accounts SET status = 'disabled' WHERE id = 'acct_1'"); err != nil {
					t.Fatalf("disable account: %v", err)
				}
			},
			token: issued.BearerToken,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testDB := cloneTokenDB(t, db)
			testService := NewService(testDB, WithClock(func() time.Time { return now }))
			if tc.setup != nil {
				tc.setup(t, testDB)
			}
			_, err := testService.VerifyBearer(ctx, tc.token)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyBearer() error = %v, want ErrInvalidToken", err)
			}
		})
	}

	expiredService := NewService(db, WithClock(func() time.Time { return now.Add(2 * time.Minute) }))
	_, err = expiredService.VerifyBearer(ctx, issued.BearerToken)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyBearer() expired error = %v, want ErrInvalidToken", err)
	}
}

func TestCreateRejectsUnsafeBoundaries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	service := NewService(db, WithClock(func() time.Time {
		return time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	}))

	for _, boundary := range []DirectoryBoundary{
		{SpaceID: "space_1", MountID: "mount_1", RelativePath: "../escape"},
		{SpaceID: "space_1", MountID: "mount_1", RelativePath: "/absolute"},
		{SpaceID: "space_1", MountID: "mount_1", RelativePath: ".omnora/private"},
		{SpaceID: "", MountID: "mount_1", RelativePath: "docs"},
	} {
		_, err := service.Create(ctx, CreateRequest{
			AccountID:  "acct_1",
			Name:       "bad boundary",
			Scopes:     []Scope{ScopeFilesList},
			Boundaries: []DirectoryBoundary{boundary},
			ExpiresAt:  time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Create(%#v) error = %v, want ErrInvalidInput", boundary, err)
		}
	}
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close sqlite: %v", err)
		}
	})
	if _, err := db.Exec(coreSchema); err != nil {
		t.Fatalf("install core schema: %v", err)
	}
	if err := InstallSchema(context.Background(), db); err != nil {
		t.Fatalf("InstallSchema() error = %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE ai_tokens ADD COLUMN credential_generation INTEGER`); err != nil {
		t.Fatalf("install credential generation column: %v", err)
	}
	return db
}

func insertActiveAccountSpaceMount(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct_1', 'ada@example.test', 'Ada', 'member', 'active')
`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space_1', 'personal', 'Ada Space', 'acct_1', 'active')
`); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status)
VALUES ('mount_1', 'space_1', 'Home', '/tmp/omnora', 'managed', 'read_only', 'active')
`); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
}

func cloneTokenDB(t *testing.T, source *sql.DB) *sql.DB {
	t.Helper()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	var secretHash, scopes, createdAt, expiresAt string
	var credentialGeneration int64
	var id, publicID, accountID, name string
	if err := source.QueryRow("SELECT id, public_id, secret_hash, account_id, name, scopes, credential_generation, created_at, expires_at FROM ai_tokens LIMIT 1").Scan(&id, &publicID, &secretHash, &accountID, &name, &scopes, &credentialGeneration, &createdAt, &expiresAt); err != nil {
		t.Fatalf("read source token: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, credential_generation, created_at, expires_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, publicID, secretHash, accountID, name, scopes, credentialGeneration, createdAt, expiresAt, createdAt); err != nil {
		t.Fatalf("copy token: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO ai_token_boundaries(token_id, space_id, mount_id, relative_path)
VALUES (?, 'space_1', 'mount_1', '')
`, id); err != nil {
		t.Fatalf("copy boundary: %v", err)
	}
	return db
}
