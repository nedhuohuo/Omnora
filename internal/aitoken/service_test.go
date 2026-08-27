package aitoken

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"omnora/internal/contentref"

	_ "modernc.org/sqlite"
)

const targetCoreSchema = `
PRAGMA foreign_keys = ON;
CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	status TEXT NOT NULL CHECK (status IN ('active', 'disabled', 'deleted')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL UNIQUE,
	root_path TEXT NOT NULL UNIQUE,
	purpose TEXT NOT NULL CHECK (purpose IN ('personal_default', 'common')),
	storage_kind TEXT NOT NULL CHECK (storage_kind IN ('managed', 'external')),
	governance TEXT NOT NULL CHECK (governance IN ('system', 'normal', 'restricted')),
	mode TEXT NOT NULL CHECK (mode IN ('read_only', 'read_write')),
	index_enabled INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'disabled', 'unavailable', 'deleted')),
	mount_identity_json TEXT,
	canonical_root_path TEXT,
	identity_key TEXT,
	mount_source_key TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE personal_directories (
	account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
	relative_path TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL CHECK (state IN ('ready', 'retained')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE mount_grants (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, account_id)
);
CREATE TABLE system_state (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO system_state(key, value) VALUES ('credential_generation', '7');
`

func TestAllowlistedScopesReplacesLegacySpaceScope(t *testing.T) {
	expected := []Scope{
		ScopeMountsRead,
		ScopeFilesList,
		ScopeFilesMetadata,
		ScopeFilesText,
		ScopeFilesDownloadTicket,
		ScopeSearchRead,
		ScopeUploadsCreate,
		ScopeFilesWrite,
		ScopeFilesTrash,
		ScopeTrashRead,
		ScopeFilesRestore,
		ScopeFilesPurge,
		ScopeSharesRead,
		ScopeSharesCreate,
		ScopeSharesRevoke,
	}
	if got := AllowlistedScopes(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("AllowlistedScopes() = %v, want %v", got, expected)
	}
	if _, err := ValidateScopes([]Scope{Scope("spaces:read")}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("legacy spaces:read error = %v, want ErrInvalidScope", err)
	}
	if got, err := ValidateScopes(expected); err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("ValidateScopes(expected) = %v, %v", got, err)
	}
	for _, scopes := range [][]Scope{nil, {}, {ScopeFilesList, ScopeFilesList}, {Scope("admin:delete")}} {
		if _, err := ValidateScopes(scopes); !errors.Is(err, ErrInvalidScope) {
			t.Fatalf("ValidateScopes(%v) error = %v, want ErrInvalidScope", scopes, err)
		}
	}
}

func TestCreateStoresAndReloadsAccountMountBoundaries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	now := testNow()
	service := NewService(db, WithClock(func() time.Time { return now }))

	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1",
		Name:      "Account MCP",
		Scopes:    []Scope{ScopeMountsRead, ScopeFilesList},
		Boundaries: []DirectoryBoundary{
			{Source: contentref.SourcePersonal, RelativePath: "docs/./notes"},
			{Source: contentref.SourceCommonMount, MountID: "common_1", RelativePath: "."},
			{Source: SourceAllAccountContent, RelativePath: "."},
		},
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if issued.Secret == "" || issued.BearerToken != issued.Token.PublicID+"."+issued.Secret {
		t.Fatalf("issued token missing one-time bearer material: %#v", issued)
	}
	if issued.Token.SecretHash == issued.Secret || !strings.HasPrefix(issued.Token.SecretHash, "sha256:") {
		t.Fatalf("SecretHash = %q, want hash only", issued.Token.SecretHash)
	}
	var storedHash string
	if err := db.QueryRow(`SELECT secret_hash FROM ai_tokens WHERE id = ?`, issued.Token.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == issued.Secret || !secretMatches(issued.Secret, storedHash) {
		t.Fatal("stored secret hash does not verify the one-time secret")
	}
	wantBoundaries := []DirectoryBoundary{
		{Source: contentref.SourcePersonal, RelativePath: "docs/notes"},
		{Source: contentref.SourceCommonMount, MountID: "common_1", RelativePath: ""},
		{Source: SourceAllAccountContent, RelativePath: ""},
	}
	if !reflect.DeepEqual(issued.Token.Boundaries, wantBoundaries) {
		t.Fatalf("issued boundaries = %#v, want %#v", issued.Token.Boundaries, wantBoundaries)
	}

	rows, err := db.QueryContext(ctx, `SELECT source, COALESCE(mount_id, ''), relative_path FROM ai_token_boundaries WHERE token_id = ? ORDER BY id`, issued.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored []DirectoryBoundary
	for rows.Next() {
		var boundary DirectoryBoundary
		if err := rows.Scan(&boundary.Source, &boundary.MountID, &boundary.RelativePath); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, boundary)
	}
	if !reflect.DeepEqual(stored, wantBoundaries) {
		t.Fatalf("stored boundaries = %#v, want %#v", stored, wantBoundaries)
	}

	principal, err := service.VerifyBearer(ctx, "Bearer "+issued.BearerToken)
	if err != nil {
		t.Fatalf("VerifyBearer() error = %v", err)
	}
	if principal.AccountID != "acct_1" || !principal.HasScope(ScopeMountsRead) || !reflect.DeepEqual(principal.Boundaries, wantBoundaries) {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestCreateValidatesBoundaryAuthorizationAtIssueTime(t *testing.T) {
	ctx := context.Background()
	now := testNow()
	request := func(boundary DirectoryBoundary) CreateRequest {
		return CreateRequest{
			AccountID: "acct_1", Name: "authorization test", Scopes: []Scope{ScopeFilesList},
			Boundaries: []DirectoryBoundary{boundary}, ExpiresAt: now.Add(time.Hour),
		}
	}
	tests := []struct {
		name     string
		boundary DirectoryBoundary
		setup    func(*testing.T, *sql.DB)
	}{
		{
			name: "personal directory not ready", boundary: DirectoryBoundary{Source: contentref.SourcePersonal},
			setup: func(t *testing.T, db *sql.DB) {
				execTest(t, db, `UPDATE personal_directories SET state = 'retained' WHERE account_id = 'acct_1'`)
			},
		},
		{
			name: "default mount inactive", boundary: DirectoryBoundary{Source: contentref.SourcePersonal},
			setup: func(t *testing.T, db *sql.DB) {
				execTest(t, db, `UPDATE mounts SET status = 'disabled' WHERE id = 'personal-default'`)
			},
		},
		{
			name: "common mount not granted", boundary: DirectoryBoundary{Source: contentref.SourceCommonMount, MountID: "common_1"},
			setup: func(t *testing.T, db *sql.DB) {
				execTest(t, db, `DELETE FROM mount_grants WHERE mount_id = 'common_1' AND account_id = 'acct_1'`)
			},
		},
		{
			name: "common mount inactive", boundary: DirectoryBoundary{Source: contentref.SourceCommonMount, MountID: "common_1"},
			setup: func(t *testing.T, db *sql.DB) {
				execTest(t, db, `UPDATE mounts SET status = 'disabled' WHERE id = 'common_1'`)
			},
		},
		{
			name: "common mount is not external", boundary: DirectoryBoundary{Source: contentref.SourceCommonMount, MountID: "common_1"},
			setup: func(t *testing.T, db *sql.DB) {
				execTest(t, db, `UPDATE mounts SET storage_kind = 'managed' WHERE id = 'common_1'`)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			insertActiveAccountModel(t, db)
			tc.setup(t, db)
			_, err := NewService(db, WithClock(func() time.Time { return now })).Create(ctx, request(tc.boundary))
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
			}
		})
	}

	db := newTestDB(t)
	insertActiveAccountOnly(t, db)
	if _, err := NewService(db, WithClock(func() time.Time { return now })).Create(ctx, request(DirectoryBoundary{Source: SourceAllAccountContent})); err != nil {
		t.Fatalf("all_account_content should require only an active account: %v", err)
	}
}

func TestCreateRejectsUnsafeOrUnsupportedAutomationBoundaries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	service := NewService(db, WithClock(testNow))

	boundaries := []DirectoryBoundary{
		{Source: contentref.SourceCollaboration, RelativePath: "docs"},
		{Source: contentref.SourcePersonal, MountID: "common_1"},
		{Source: contentref.SourceCommonMount},
		{Source: SourceAllAccountContent, MountID: "common_1"},
		{Source: SourceAllAccountContent, RelativePath: "docs"},
		{Source: contentref.SourcePersonal, RelativePath: "/absolute"},
		{Source: contentref.SourcePersonal, RelativePath: `docs\notes`},
		{Source: contentref.SourcePersonal, RelativePath: "../escape"},
		{Source: contentref.SourcePersonal, RelativePath: "docs/../escape"},
		{Source: contentref.SourcePersonal, RelativePath: ".omnora/private"},
		{Source: contentref.SourcePersonal, RelativePath: "docs/.omnora/private"},
	}
	for _, boundary := range boundaries {
		_, err := service.Create(ctx, CreateRequest{
			AccountID: "acct_1", Name: "bad boundary", Scopes: []Scope{ScopeFilesList},
			Boundaries: []DirectoryBoundary{boundary}, ExpiresAt: testNow().Add(time.Hour),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Create(%#v) error = %v, want ErrInvalidInput", boundary, err)
		}
	}
}

func TestRefreshPrincipalDoesNotFreezeCommonMountGrant(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	now := testNow()
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1", Name: "dynamic authorization", Scopes: []Scope{ScopeFilesList},
		Boundaries: []DirectoryBoundary{{Source: contentref.SourceCommonMount, MountID: "common_1"}},
		ExpiresAt:  now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, `DELETE FROM mount_grants WHERE mount_id = 'common_1' AND account_id = 'acct_1'`)
	principal, err := service.RefreshPrincipal(ctx, issued.Token.ID)
	if err != nil {
		t.Fatalf("RefreshPrincipal() after grant removal error = %v", err)
	}
	if !reflect.DeepEqual(principal.Boundaries, issued.Token.Boundaries) {
		t.Fatalf("refreshed boundaries = %#v, want %#v", principal.Boundaries, issued.Token.Boundaries)
	}
}

func TestVerifyAndRefreshRejectTamperedOrMissingStoredBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		path *string
	}{
		{name: "parent component", path: stringPointer("../escape")},
		{name: "reserved metadata", path: stringPointer(".omnora/private")},
		{name: "non canonical", path: stringPointer("docs/./notes")},
		{name: "missing boundary", path: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			insertActiveAccountModel(t, db)
			service, issued := issuePersonalToken(t, db, testNow())
			if tc.path == nil {
				execTest(t, db, `DELETE FROM ai_token_boundaries WHERE token_id = ?`, issued.Token.ID)
			} else {
				execTest(t, db, `UPDATE ai_token_boundaries SET relative_path = ? WHERE token_id = ?`, *tc.path, issued.Token.ID)
			}

			if _, err := service.VerifyBearer(ctx, issued.BearerToken); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyBearer() error = %v, want ErrInvalidToken", err)
			}
			if _, err := service.RefreshPrincipal(ctx, issued.Token.ID); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("RefreshPrincipal() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestCredentialGenerationExpiryAndInactiveAccountRemainEnforced(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	now := testNow()
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1", Name: "credential state", Scopes: []Scope{ScopeFilesText},
		Boundaries: []DirectoryBoundary{{Source: contentref.SourcePersonal}}, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := db.QueryRow(`SELECT credential_generation FROM ai_tokens WHERE id = ?`, issued.Token.ID).Scan(&generation); err != nil || generation != 7 {
		t.Fatalf("credential_generation = %d, error = %v", generation, err)
	}
	execTest(t, db, `UPDATE system_state SET value = '8' WHERE key = 'credential_generation'`)
	if _, err := service.VerifyBearer(ctx, issued.BearerToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("generation bump error = %v, want ErrInvalidToken", err)
	}

	db2 := newTestDB(t)
	insertActiveAccountModel(t, db2)
	service2 := NewService(db2, WithClock(func() time.Time { return now }))
	issued2, err := service2.Create(ctx, CreateRequest{
		AccountID: "acct_1", Name: "expiration", Scopes: []Scope{ScopeFilesText},
		Boundaries: []DirectoryBoundary{{Source: contentref.SourcePersonal}}, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredService := NewService(db2, WithClock(func() time.Time { return now.Add(2 * time.Minute) }))
	if _, err := expiredService.VerifyBearer(ctx, issued2.BearerToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired error = %v, want ErrInvalidToken", err)
	}
	if _, err := expiredService.RefreshPrincipal(ctx, issued2.Token.ID); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired refresh error = %v, want ErrInvalidToken", err)
	}
	execTest(t, db2, `UPDATE accounts SET status = 'disabled' WHERE id = 'acct_1'`)
	if _, err := service2.RefreshPrincipal(ctx, issued2.Token.ID); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("inactive account refresh error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyBearerSecurityAndUsageRegressions(t *testing.T) {
	ctx := context.Background()
	now := testNow()

	t.Run("wrong secret", func(t *testing.T) {
		db := newTestDB(t)
		insertActiveAccountModel(t, db)
		service, issued := issuePersonalToken(t, db, now)
		if _, err := service.VerifyBearer(ctx, issued.Token.PublicID+".wrong"); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("VerifyBearer() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		db := newTestDB(t)
		insertActiveAccountModel(t, db)
		service, issued := issuePersonalToken(t, db, now)
		if err := service.Revoke(ctx, issued.Token.ID); err != nil {
			t.Fatalf("Revoke() error = %v", err)
		}
		if _, err := service.VerifyBearer(ctx, issued.BearerToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("VerifyBearer() error = %v, want ErrInvalidToken", err)
		}
		if _, err := service.RefreshPrincipal(ctx, issued.Token.ID); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("RefreshPrincipal() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("inactive account", func(t *testing.T) {
		db := newTestDB(t)
		insertActiveAccountModel(t, db)
		service, issued := issuePersonalToken(t, db, now)
		execTest(t, db, `UPDATE accounts SET status = 'disabled' WHERE id = 'acct_1'`)
		if _, err := service.VerifyBearer(ctx, issued.BearerToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("VerifyBearer() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("malformed bearer", func(t *testing.T) {
		db := newTestDB(t)
		service := NewService(db, WithClock(func() time.Time { return now }))
		for _, bearer := range []string{"", "Bearer", "Bearer public", "public.", ".secret", "public.secret.extra", "public. secret", "public\n.secret"} {
			if _, err := service.VerifyBearer(ctx, bearer); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyBearer(%q) error = %v, want ErrInvalidToken", bearer, err)
			}
		}
	})

	t.Run("last used at", func(t *testing.T) {
		db := newTestDB(t)
		insertActiveAccountModel(t, db)
		service, issued := issuePersonalToken(t, db, now)
		if _, err := service.VerifyBearer(ctx, "Bearer "+issued.BearerToken); err != nil {
			t.Fatalf("VerifyBearer() error = %v", err)
		}
		var lastUsedAt string
		if err := db.QueryRow(`SELECT last_used_at FROM ai_tokens WHERE id = ?`, issued.Token.ID).Scan(&lastUsedAt); err != nil {
			t.Fatal(err)
		}
		if lastUsedAt != formatTime(now) {
			t.Fatalf("last_used_at = %q, want %q", lastUsedAt, formatTime(now))
		}
	})
}

func TestCreateSecureRollsBackTokenAndAuditTogether(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	execTest(t, db, `CREATE TABLE audit_probe(event TEXT NOT NULL)`)
	service := NewService(db, WithClock(testNow))
	auditErr := errors.New("audit unavailable")
	_, err := service.CreateSecure(ctx, CreateRequest{
		AccountID: "acct_1", Name: "atomic create", Scopes: []Scope{ScopeFilesList},
		Boundaries: []DirectoryBoundary{{Source: contentref.SourcePersonal}}, ExpiresAt: testNow().Add(time.Hour),
	}, func(ctx context.Context, tx *sql.Tx, token Token) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_probe(event) VALUES (?)`, token.ID); err != nil {
			return err
		}
		return auditErr
	})
	if !errors.Is(err, auditErr) {
		t.Fatalf("CreateSecure() error = %v, want audit error", err)
	}
	assertRowCount(t, db, "ai_tokens", 0)
	assertRowCount(t, db, "ai_token_boundaries", 0)
	assertRowCount(t, db, "audit_probe", 0)
}

func TestRevokeSecureRollsBackRevocationAndAuditTogether(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	execTest(t, db, `CREATE TABLE audit_probe(event TEXT NOT NULL)`)
	service, issued := issuePersonalToken(t, db, testNow())
	auditErr := errors.New("audit unavailable")
	err := service.RevokeSecure(ctx, issued.Token.ID, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_probe(event) VALUES ('revoke')`); err != nil {
			return err
		}
		return auditErr
	})
	if !errors.Is(err, auditErr) {
		t.Fatalf("RevokeSecure() error = %v, want audit error", err)
	}
	var revokedAt sql.NullString
	if err := db.QueryRow(`SELECT revoked_at FROM ai_tokens WHERE id = ?`, issued.Token.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt.Valid {
		t.Fatalf("revoked_at = %q, want NULL after rollback", revokedAt.String)
	}
	assertRowCount(t, db, "audit_probe", 0)
	if _, err := service.VerifyBearer(ctx, issued.BearerToken); err != nil {
		t.Fatalf("token should remain valid after audit rollback: %v", err)
	}
}

func TestRefreshPrincipalReloadsCurrentScopesAndBoundaries(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	service, issued := issuePersonalToken(t, db, testNow())
	execTest(t, db, `UPDATE ai_tokens SET scopes = '["files:trash"]' WHERE id = ?`, issued.Token.ID)
	execTest(t, db, `DELETE FROM ai_token_boundaries WHERE token_id = ?`, issued.Token.ID)
	execTest(t, db, `INSERT INTO ai_token_boundaries(token_id, source, mount_id, relative_path) VALUES (?, 'common_mount', 'common_1', 'refreshed')`, issued.Token.ID)

	principal, err := service.RefreshPrincipal(ctx, issued.Token.ID)
	if err != nil {
		t.Fatalf("RefreshPrincipal() error = %v", err)
	}
	if !principal.HasScope(ScopeFilesTrash) || principal.HasScope(ScopeFilesList) {
		t.Fatalf("refreshed scopes = %v", principal.Scopes)
	}
	want := []DirectoryBoundary{{Source: contentref.SourceCommonMount, MountID: "common_1", RelativePath: "refreshed"}}
	if !reflect.DeepEqual(principal.Boundaries, want) {
		t.Fatalf("refreshed boundaries = %#v, want %#v", principal.Boundaries, want)
	}
}

func TestCreateWithoutExpiryNeverExpires(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountModel(t, db)
	now := testNow()
	issued, err := NewService(db, WithClock(func() time.Time { return now })).Create(ctx, CreateRequest{
		AccountID: "acct_1", Name: "never expires", Scopes: []Scope{ScopeFilesList},
		Boundaries: []DirectoryBoundary{{Source: contentref.SourcePersonal}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !issued.Token.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want zero", issued.Token.ExpiresAt)
	}
	farFuture := NewService(db, WithClock(func() time.Time { return now.Add(100 * 365 * 24 * time.Hour) }))
	if _, err := farFuture.VerifyBearer(ctx, issued.BearerToken); err != nil {
		t.Fatalf("VerifyBearer() far in future error = %v", err)
	}
	if _, err := farFuture.RefreshPrincipal(ctx, issued.Token.ID); err != nil {
		t.Fatalf("RefreshPrincipal() far in future error = %v", err)
	}
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "aitoken.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(targetCoreSchema); err != nil {
		t.Fatalf("install target core schema: %v", err)
	}
	if err := InstallSchema(context.Background(), db); err != nil {
		t.Fatalf("InstallSchema() error = %v", err)
	}
	return db
}

func insertActiveAccountOnly(t *testing.T, db *sql.DB) {
	t.Helper()
	execTest(t, db, `INSERT INTO accounts(id, email, display_name, role, status) VALUES ('acct_1', 'ada@example.test', 'Ada', 'member', 'active')`)
}

func insertActiveAccountModel(t *testing.T, db *sql.DB) {
	t.Helper()
	insertActiveAccountOnly(t, db)
	execTest(t, db, `INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status) VALUES ('personal-default', 'Personal Files', 'personal', 'personal_default', 'managed', 'system', 'read_write', 'active')`)
	execTest(t, db, `INSERT INTO personal_directories(account_id, relative_path, state) VALUES ('acct_1', 'acct_1', 'ready')`)
	execTest(t, db, `INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, status) VALUES ('common_1', 'Shared Docs', '/srv/shared', 'common', 'external', 'normal', 'read_write', 'active')`)
	execTest(t, db, `INSERT INTO mount_grants(mount_id, account_id, permission) VALUES ('common_1', 'acct_1', 'editor')`)
}

func execTest(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

func assertRowCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(1) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s row count = %d, want %d", table, got, want)
	}
}

func testNow() time.Time {
	return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
}

func stringPointer(value string) *string {
	return &value
}

func issuePersonalToken(t *testing.T, db *sql.DB, now time.Time) (*Service, IssuedToken) {
	t.Helper()
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.Create(context.Background(), CreateRequest{
		AccountID: "acct_1", Name: "security regression", Scopes: []Scope{ScopeFilesList},
		Boundaries: []DirectoryBoundary{{Source: contentref.SourcePersonal}}, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return service, issued
}
