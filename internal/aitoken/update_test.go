package aitoken

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"omnora/internal/domain"
)

func TestCreateSpaceGrantSecurePersistsAndLoadsCurrentGrants(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	root := insertActiveAccountSpaceMount(t, db)
	denyPath := filepath.Join(root, "private", "docs")
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))

	issued, err := service.CreateSpaceGrantSecure(ctx, CreateSpaceGrantRequest{
		AccountID: "acct_1",
		Name:      "space token",
		Scopes:    []Scope{ScopeFilesList, ScopeFilesWrite},
		SpaceGrants: []SpaceGrant{{
			SpaceID:    "space_1",
			Permission: domain.SpacePermissionEditor,
			FolderDenylist: []FolderDeny{{
				MountID: "mount_1", RelativePath: "private/./docs", IdentityFingerprint: testDirectoryFingerprint(t, denyPath),
			}},
		}},
		ExpiresAt: now.Add(time.Hour),
	}, nil)
	if err != nil {
		t.Fatalf("CreateSpaceGrantSecure() error = %v", err)
	}
	if issued.Token.AuthorizationModel != AuthorizationModelSpaceGrant || len(issued.Token.Boundaries) != 0 {
		t.Fatalf("issued token model/boundaries = %q/%#v", issued.Token.AuthorizationModel, issued.Token.Boundaries)
	}
	wantGrants := []SpaceGrant{{
		SpaceID:    "space_1",
		Permission: domain.SpacePermissionEditor,
		FolderDenylist: []FolderDeny{{
			MountID: "mount_1", RelativePath: "private/docs", IdentityFingerprint: testDirectoryFingerprint(t, denyPath), Status: FolderDenyStatusActive,
		}},
	}}
	if !reflect.DeepEqual(issued.Token.SpaceGrants, wantGrants) {
		t.Fatalf("issued grants = %#v, want %#v", issued.Token.SpaceGrants, wantGrants)
	}

	principal, err := service.VerifyBearer(ctx, issued.BearerToken)
	if err != nil {
		t.Fatalf("VerifyBearer() error = %v", err)
	}
	if principal.AuthorizationModel != AuthorizationModelSpaceGrant || !reflect.DeepEqual(principal.SpaceGrants, wantGrants) || len(principal.Boundaries) != 0 {
		t.Fatalf("principal = %#v", principal)
	}

	var model, permission, path, identity, status string
	if err := db.QueryRowContext(ctx, `
SELECT t.authorization_model, g.permission, d.relative_path, d.identity_fingerprint, d.status
FROM ai_tokens t
JOIN ai_token_space_grants g ON g.token_id = t.id
JOIN ai_token_folder_denies d ON d.token_id = t.id AND d.space_id = g.space_id
WHERE t.id = ?
`, issued.Token.ID).Scan(&model, &permission, &path, &identity, &status); err != nil {
		t.Fatalf("query persisted grant: %v", err)
	}
	if model != "space_grant" || permission != "editor" || path != "private/docs" || identity != testDirectoryFingerprint(t, denyPath) || status != "active" {
		t.Fatalf("persisted values = %q %q %q %q %q", model, permission, path, identity, status)
	}
}

func TestVerifySpaceGrantDirectoriesCapturesFingerprintForAPIPaths(t *testing.T) {
	db := newTestDB(t)
	root := insertActiveAccountSpaceMount(t, db)
	if err := os.MkdirAll(filepath.Join(root, "private"), 0o755); err != nil {
		t.Fatalf("create deny directory: %v", err)
	}
	service := NewService(db)
	verified, err := service.VerifySpaceGrantDirectories(context.Background(), []SpaceGrant{{
		SpaceID: "space_1", Permission: domain.SpacePermissionViewer,
		FolderDenylist: []FolderDeny{{MountID: "mount_1", RelativePath: "private"}},
	}})
	if err != nil {
		t.Fatalf("VerifySpaceGrantDirectories() error = %v", err)
	}
	if got := verified[0].FolderDenylist[0].IdentityFingerprint; got == "" {
		t.Fatal("VerifySpaceGrantDirectories() did not capture a directory fingerprint")
	}
}

func TestSpaceGrantCreationEnforcesLiveAccountPermissionAndMountOwnership(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	root := insertActiveAccountSpaceMount(t, db)
	service := NewService(db)
	if _, err := db.ExecContext(ctx, `UPDATE space_members SET permission = 'viewer' WHERE space_id = 'space_1' AND account_id = 'acct_1'`); err != nil {
		t.Fatalf("downgrade member: %v", err)
	}

	base := CreateSpaceGrantRequest{
		AccountID: "acct_1", Name: "permission test", Scopes: []Scope{ScopeFilesList},
		SpaceGrants: []SpaceGrant{{SpaceID: "space_1", Permission: domain.SpacePermissionEditor}},
	}
	if _, err := service.CreateSpaceGrantSecure(ctx, base, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("editor grant for viewer error = %v, want ErrInvalidInput", err)
	}
	base.SpaceGrants[0].Permission = domain.SpacePermissionViewer
	base.SpaceGrants[0].FolderDenylist = []FolderDeny{{MountID: "mount-missing", RelativePath: "private", IdentityFingerprint: testDirectoryFingerprint(t, filepath.Join(root, "private"))}}
	if _, err := service.CreateSpaceGrantSecure(ctx, base, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("deny for missing mount error = %v, want ErrInvalidInput", err)
	}
	base.SpaceGrants[0].FolderDenylist[0].MountID = "mount_1"
	if _, err := service.CreateSpaceGrantSecure(ctx, base, nil); err != nil {
		t.Fatalf("viewer grant for viewer error = %v", err)
	}
}

func TestSpaceGrantDenyValidationRejectsEmptyForgedMissingFileAndSymlink(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	root := insertActiveAccountSpaceMount(t, db)
	filePath := filepath.Join(root, "private", "document.txt")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create test file: %v", err)
	}
	linkPath := filepath.Join(root, "private", "link")
	if err := os.Symlink(filepath.Join(root, "private"), linkPath); err != nil {
		t.Fatalf("create test symlink: %v", err)
	}
	service := NewService(db)
	validFingerprint := testDirectoryFingerprint(t, filepath.Join(root, "private", "docs"))
	tests := []struct {
		name        string
		path        string
		fingerprint string
	}{
		{name: "empty fingerprint", path: "private/docs"},
		{name: "forged fingerprint", path: "private/docs", fingerprint: "dev:1:ino:2"},
		{name: "missing directory", path: "private/missing", fingerprint: validFingerprint},
		{name: "regular file", path: "private/document.txt", fingerprint: testDirectoryFingerprint(t, filePath)},
		{name: "symlink directory", path: "private/link", fingerprint: validFingerprint},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.CreateSpaceGrantSecure(ctx, CreateSpaceGrantRequest{
				AccountID: "acct_1", Name: tc.name, Scopes: []Scope{ScopeFilesList},
				SpaceGrants: []SpaceGrant{{
					SpaceID: "space_1", Permission: domain.SpacePermissionViewer,
					FolderDenylist: []FolderDeny{{MountID: "mount_1", RelativePath: tc.path, IdentityFingerprint: tc.fingerprint}},
				}},
			}, nil)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateSpaceGrantSecure() error = %v, want ErrInvalidInput", err)
			}
		})
	}

	issued, err := service.CreateSpaceGrantSecure(ctx, CreateSpaceGrantRequest{
		AccountID: "acct_1", Name: "valid deny", Scopes: []Scope{ScopeFilesList},
		SpaceGrants: []SpaceGrant{{
			SpaceID: "space_1", Permission: domain.SpacePermissionViewer,
			FolderDenylist: []FolderDeny{{MountID: "mount_1", RelativePath: "private/docs", IdentityFingerprint: validFingerprint}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("valid CreateSpaceGrantSecure() error = %v", err)
	}
	for _, tc := range tests {
		t.Run("update/"+tc.name, func(t *testing.T) {
			_, err := service.UpdateSecure(ctx, UpdateRequest{
				AccountID: "acct_1", TokenID: issued.Token.ID, Name: tc.name, Scopes: []Scope{ScopeFilesList},
				SpaceGrants: []SpaceGrant{{
					SpaceID: "space_1", Permission: domain.SpacePermissionViewer,
					FolderDenylist: []FolderDeny{{MountID: "mount_1", RelativePath: tc.path, IdentityFingerprint: tc.fingerprint}},
				}},
			}, nil)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("UpdateSecure() error = %v, want ErrInvalidInput", err)
			}
		})
	}
	var storedFingerprint string
	if err := db.QueryRowContext(ctx, `SELECT identity_fingerprint FROM ai_token_folder_denies WHERE token_id = ?`, issued.Token.ID).Scan(&storedFingerprint); err != nil {
		t.Fatalf("query stored fingerprint: %v", err)
	}
	if storedFingerprint != validFingerprint || storedFingerprint == "" {
		t.Fatalf("stored fingerprint = %q, want real %q", storedFingerprint, validFingerprint)
	}
}

func TestUpdateSecureAtomicallyReplacesGrantsAndPreservesBearerSecret(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	secondRoot := insertSecondSpaceMount(t, db)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.CreateSpaceGrantSecure(ctx, CreateSpaceGrantRequest{
		AccountID: "acct_1", Name: "before", Scopes: []Scope{ScopeFilesList},
		SpaceGrants: []SpaceGrant{{SpaceID: "space_1", Permission: domain.SpacePermissionViewer}},
		ExpiresAt:   now.Add(time.Hour),
	}, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	updated, err := service.UpdateSecure(ctx, UpdateRequest{
		AccountID: "acct_1", TokenID: issued.Token.ID, Name: "after", Scopes: []Scope{ScopeFilesWrite},
		SpaceGrants: []SpaceGrant{{
			SpaceID: "space_2", Permission: domain.SpacePermissionEditor,
			FolderDenylist: []FolderDeny{{MountID: "mount_2", RelativePath: "archive", IdentityFingerprint: testDirectoryFingerprint(t, filepath.Join(secondRoot, "archive"))}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("UpdateSecure() error = %v", err)
	}
	if updated.PublicID != issued.Token.PublicID || updated.SecretHash != issued.Token.SecretHash || updated.Name != "after" {
		t.Fatalf("updated token changed bearer identity or missed metadata: %#v", updated)
	}
	principal, err := service.VerifyBearer(ctx, issued.BearerToken)
	if err != nil {
		t.Fatalf("VerifyBearer() after update error = %v", err)
	}
	if principal.AuthorizationModel != AuthorizationModelSpaceGrant || !principal.HasScope(ScopeFilesWrite) || len(principal.SpaceGrants) != 1 || principal.SpaceGrants[0].SpaceID != "space_2" {
		t.Fatalf("refreshed principal after update = %#v", principal)
	}
	var updatedFingerprint string
	if err := db.QueryRowContext(ctx, `SELECT identity_fingerprint FROM ai_token_folder_denies WHERE token_id = ?`, issued.Token.ID).Scan(&updatedFingerprint); err != nil {
		t.Fatalf("query updated fingerprint: %v", err)
	}
	wantFingerprint := testDirectoryFingerprint(t, filepath.Join(secondRoot, "archive"))
	if updatedFingerprint != wantFingerprint || updatedFingerprint == "" {
		t.Fatalf("updated fingerprint = %q, want real %q", updatedFingerprint, wantFingerprint)
	}

	auditErr := errors.New("audit unavailable")
	if _, err := service.UpdateSecure(ctx, UpdateRequest{
		AccountID: "acct_1", TokenID: issued.Token.ID, Name: "must roll back", Scopes: []Scope{ScopeFilesText},
		SpaceGrants: []SpaceGrant{{SpaceID: "space_1", Permission: domain.SpacePermissionViewer}},
	}, func(context.Context, *sql.Tx, Token) error { return auditErr }); !errors.Is(err, auditErr) {
		t.Fatalf("UpdateSecure() audit error = %v, want %v", err, auditErr)
	}
	principal, err = service.RefreshPrincipal(ctx, issued.Token.ID)
	if err != nil {
		t.Fatalf("RefreshPrincipal() after rollback error = %v", err)
	}
	if !principal.HasScope(ScopeFilesWrite) || principal.HasScope(ScopeFilesText) || len(principal.SpaceGrants) != 1 || principal.SpaceGrants[0].SpaceID != "space_2" {
		t.Fatalf("rollback did not preserve prior state: %#v", principal)
	}
	var storedName string
	if err := db.QueryRowContext(ctx, `SELECT name FROM ai_tokens WHERE id = ?`, issued.Token.ID).Scan(&storedName); err != nil {
		t.Fatalf("query name after rollback: %v", err)
	}
	if storedName != "after" {
		t.Fatalf("name after rollback = %q, want after", storedName)
	}
}

func TestUpdateSecureConvertsLegacyTokenAndAuditFailureRollsBackConversion(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	root := insertActiveAccountSpaceMount(t, db)
	denyPath := filepath.Join(root, "private", "docs")
	service := NewService(db)
	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1", Name: "legacy token", Scopes: []Scope{ScopeFilesList},
		Boundaries: []DirectoryBoundary{{SpaceID: "space_1", MountID: "mount_1", RelativePath: "private"}},
	})
	if err != nil {
		t.Fatalf("Create() legacy token error = %v", err)
	}
	grant := []SpaceGrant{{
		SpaceID: "space_1", Permission: domain.SpacePermissionViewer,
		FolderDenylist: []FolderDeny{{MountID: "mount_1", RelativePath: "private/docs", IdentityFingerprint: testDirectoryFingerprint(t, denyPath)}},
	}}
	auditErr := errors.New("audit unavailable")
	if _, err := service.UpdateSecure(ctx, UpdateRequest{
		AccountID: "acct_1", TokenID: issued.Token.ID, Name: "converted", Scopes: []Scope{ScopeFilesList}, SpaceGrants: grant,
	}, func(context.Context, *sql.Tx, Token) error { return auditErr }); !errors.Is(err, auditErr) {
		t.Fatalf("legacy conversion audit error = %v, want %v", err, auditErr)
	}
	var model string
	if err := db.QueryRowContext(ctx, `SELECT authorization_model FROM ai_tokens WHERE id = ?`, issued.Token.ID).Scan(&model); err != nil {
		t.Fatalf("query model after rollback: %v", err)
	}
	if model != string(AuthorizationModelLegacyBoundary) {
		t.Fatalf("model after audit rollback = %q, want legacy_boundary", model)
	}
	var boundaryCount, grantCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM ai_token_boundaries WHERE token_id = ?`, issued.Token.ID).Scan(&boundaryCount); err != nil {
		t.Fatalf("query legacy boundaries after rollback: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM ai_token_space_grants WHERE token_id = ?`, issued.Token.ID).Scan(&grantCount); err != nil {
		t.Fatalf("query grants after rollback: %v", err)
	}
	if boundaryCount != 1 || grantCount != 0 {
		t.Fatalf("legacy state after rollback = boundaries %d grants %d, want 1/0", boundaryCount, grantCount)
	}

	updated, err := service.UpdateSecure(ctx, UpdateRequest{
		AccountID: "acct_1", TokenID: issued.Token.ID, Name: "converted", Scopes: []Scope{ScopeFilesList}, SpaceGrants: grant,
	}, nil)
	if err != nil {
		t.Fatalf("successful legacy conversion error = %v", err)
	}
	if updated.AuthorizationModel != AuthorizationModelSpaceGrant || len(updated.Boundaries) != 0 || len(updated.SpaceGrants) != 1 {
		t.Fatalf("converted token = %#v", updated)
	}
	principal, err := service.VerifyBearer(ctx, issued.BearerToken)
	if err != nil {
		t.Fatalf("VerifyBearer() after legacy conversion error = %v", err)
	}
	if principal.AuthorizationModel != AuthorizationModelSpaceGrant || len(principal.Boundaries) != 0 || len(principal.SpaceGrants) != 1 {
		t.Fatalf("principal after legacy conversion = %#v", principal)
	}
}

func TestRefreshPrincipalReadsOneAuthorizationVersionFromConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	db := newConcurrentTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	service := NewService(db)
	issued, err := service.Create(ctx, CreateRequest{
		AccountID: "acct_1", Name: "concurrent token", Scopes: []Scope{ScopeFilesList},
		Boundaries: []DirectoryBoundary{{SpaceID: "space_1", MountID: "mount_1", RelativePath: "legacy-v1"}},
	})
	if err != nil {
		t.Fatalf("create legacy token: %v", err)
	}

	var writerWG sync.WaitGroup
	writerErr := make(chan error, 1)
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for iteration := 0; iteration < 80; iteration++ {
			version := iteration % 2
			if err := updateAuthorizationVersionWithSQLiteRetry(ctx, db, issued.Token.ID, version); err != nil {
				writerErr <- err
				return
			}
		}
	}()

	for iteration := 0; iteration < 240; iteration++ {
		var principal Principal
		var err error
		if iteration%2 == 0 {
			principal, err = refreshPrincipalWithSQLiteRetry(ctx, service, issued.Token.ID)
		} else {
			principal, err = verifyBearerWithSQLiteRetry(ctx, service, issued.BearerToken)
		}
		if err != nil {
			writerWG.Wait()
			t.Fatalf("authorization load iteration %d error = %v", iteration, err)
		}
		switch principal.AuthorizationModel {
		case AuthorizationModelLegacyBoundary:
			if len(principal.Boundaries) != 1 || principal.Boundaries[0].RelativePath != "legacy-v1" || len(principal.SpaceGrants) != 0 {
				t.Fatalf("mixed legacy authorization at iteration %d: %#v", iteration, principal)
			}
		case AuthorizationModelSpaceGrant:
			if len(principal.Boundaries) != 0 || len(principal.SpaceGrants) != 1 || principal.SpaceGrants[0].SpaceID != "space_1" {
				t.Fatalf("mixed space authorization at iteration %d: %#v", iteration, principal)
			}
		default:
			t.Fatalf("unknown authorization model at iteration %d: %q", iteration, principal.AuthorizationModel)
		}
	}
	writerWG.Wait()
	select {
	case err := <-writerErr:
		t.Fatalf("concurrent authorization writer error = %v", err)
	default:
	}
}

func updateAuthorizationVersionWithSQLiteRetry(ctx context.Context, db *sql.DB, tokenID string, version int) error {
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		tx, err := db.BeginTx(ctx, nil)
		if err == nil {
			if _, err = tx.ExecContext(ctx, `DELETE FROM ai_token_folder_denies WHERE token_id = ?`, tokenID); err == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM ai_token_space_grants WHERE token_id = ?`, tokenID)
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM ai_token_boundaries WHERE token_id = ?`, tokenID)
			}
			if err == nil && version == 0 {
				_, err = tx.ExecContext(ctx, `UPDATE ai_tokens SET authorization_model = 'legacy_boundary' WHERE id = ?`, tokenID)
			}
			if err == nil && version == 0 {
				_, err = tx.ExecContext(ctx, `INSERT INTO ai_token_boundaries(token_id, space_id, mount_id, relative_path) VALUES (?, 'space_1', 'mount_1', 'legacy-v1')`, tokenID)
			}
			if err == nil && version == 1 {
				_, err = tx.ExecContext(ctx, `UPDATE ai_tokens SET authorization_model = 'space_grant' WHERE id = ?`, tokenID)
			}
			if err == nil && version == 1 {
				_, err = tx.ExecContext(ctx, `INSERT INTO ai_token_space_grants(token_id, space_id, permission) VALUES (?, 'space_1', 'viewer')`, tokenID)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err == nil {
			return nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "locked") {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return lastErr
}

func refreshPrincipalWithSQLiteRetry(ctx context.Context, service *Service, tokenID string) (Principal, error) {
	return retryLockedPrincipal(func() (Principal, error) {
		return service.RefreshPrincipal(ctx, tokenID)
	})
}

func verifyBearerWithSQLiteRetry(ctx context.Context, service *Service, bearerToken string) (Principal, error) {
	return retryLockedPrincipal(func() (Principal, error) {
		return service.VerifyBearer(ctx, bearerToken)
	})
}

func retryLockedPrincipal(load func() (Principal, error)) (Principal, error) {
	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		principal, err := load()
		if err == nil {
			return principal, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "locked") {
			return Principal{}, err
		}
		time.Sleep(time.Millisecond)
	}
	return Principal{}, lastErr
}

func TestSpaceGrantLoadingFailsClosedForUnknownModelAndMissingGrant(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	insertActiveAccountSpaceMount(t, db)
	service := NewService(db)
	issued, err := service.CreateSpaceGrantSecure(ctx, CreateSpaceGrantRequest{
		AccountID: "acct_1", Name: "fail closed", Scopes: []Scope{ScopeFilesList},
		SpaceGrants: []SpaceGrant{{SpaceID: "space_1", Permission: domain.SpacePermissionViewer}},
	}, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM ai_token_space_grants WHERE token_id = ?`, issued.Token.ID); err != nil {
		t.Fatalf("delete grants: %v", err)
	}
	if _, err := service.RefreshPrincipal(ctx, issued.Token.ID); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("RefreshPrincipal() without grants error = %v, want ErrInvalidToken", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("disable check constraints: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ai_tokens SET authorization_model = 'future_model' WHERE id = ?`, issued.Token.ID); err != nil {
		t.Fatalf("set unknown model: %v", err)
	}
	if _, err := service.RefreshPrincipal(ctx, issued.Token.ID); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("RefreshPrincipal() unknown model error = %v, want ErrInvalidToken", err)
	}
}

func insertSecondSpaceMount(t *testing.T, db *sql.DB) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "archive"), 0o755); err != nil {
		t.Fatalf("create second test mount directories: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space_2', 'shared', 'Second Space', NULL, 'active');
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space_2', 'acct_1', 'manager');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status)
VALUES ('mount_2', 'space_2', 'Second Mount', ?, 'managed', 'read_write', 'active');
`, root); err != nil {
		t.Fatalf("insert second space: %v", err)
	}
	return root
}

func testDirectoryFingerprint(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		t.Fatalf("stat_t unavailable for %s", path)
	}
	return fmt.Sprintf("dev:%d:ino:%d", uint64(stat.Dev), uint64(stat.Ino))
}
