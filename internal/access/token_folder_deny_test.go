package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"omnora/internal/aitoken"
	"omnora/internal/domain"
	"omnora/internal/mountid"
)

func issueSpaceGrantGuardToken(
	t *testing.T,
	f guardFixture,
	accountID string,
	permission domain.SpacePermission,
	denies []aitoken.FolderDeny,
) aitoken.Principal {
	t.Helper()
	issued, err := aitoken.NewService(f.db).CreateSpaceGrant(context.Background(), aitoken.CreateSpaceGrantRequest{
		AccountID: accountID,
		Name:      "space grant guard test",
		Scopes:    aitoken.AllowlistedScopes(),
		SpaceGrants: []aitoken.SpaceGrant{{
			SpaceID:        f.spaceID,
			Permission:     permission,
			FolderDenylist: denies,
		}},
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("issue space-grant token: %v", err)
	}
	return aitoken.Principal{
		AccountID:          issued.Token.AccountID,
		TokenID:            issued.Token.ID,
		PublicID:           issued.Token.PublicID,
		Scopes:             issued.Token.Scopes,
		AuthorizationModel: issued.Token.AuthorizationModel,
		SpaceGrants:        issued.Token.SpaceGrants,
		ExpiresAt:          issued.Token.ExpiresAt,
	}
}

func spaceGrantRequest(
	f guardFixture,
	principal *aitoken.Principal,
	path string,
	required domain.SpacePermission,
	write bool,
	recursive bool,
) CheckRequest {
	scope := aitoken.ScopeFilesList
	if write {
		scope = aitoken.ScopeFilesWrite
	}
	return CheckRequest{
		Subject:            Subject{AccountID: principal.AccountID, Principal: principal},
		Scope:              scope,
		Locator:            Locator{SpaceID: f.spaceID, MountID: f.mountID, Path: path},
		RequiredPermission: required,
		Write:              write,
		Recursive:          recursive,
	}
}

func createDenyDirectory(t *testing.T, f guardFixture, relativePath string) aitoken.FolderDeny {
	t.Helper()
	target := filepath.Join(f.root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create deny directory: %v", err)
	}
	return aitoken.FolderDeny{
		MountID:             f.mountID,
		RelativePath:        relativePath,
		IdentityFingerprint: directoryFingerprint(t, target),
		Status:              aitoken.FolderDenyStatusActive,
	}
}

func directoryFingerprint(t *testing.T, target string) string {
	t.Helper()
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat directory: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		t.Fatal("directory stat did not expose syscall.Stat_t")
	}
	return fmt.Sprintf("dev:%d:ino:%d", uint64(stat.Dev), uint64(stat.Ino))
}

func insertActiveMount(t *testing.T, f guardFixture, mountID, spaceID string) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".guard-future-mount-")
	if err != nil {
		t.Fatalf("create future mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolve future mount root: %v", err)
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture future mount identity: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal future mount identity: %v", err)
	}
	if _, err := f.db.Exec(`
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status, mount_identity_json)
VALUES (?, ?, ?, ?, 'managed', 'read_write', 'active', ?)
`, mountID, spaceID, mountID, root, string(identityJSON)); err != nil {
		t.Fatalf("insert future mount: %v", err)
	}
	return root
}

func TestGuardSpaceGrantPermissionIntersectsLiveAccountACL(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	ctx := context.Background()

	viewer := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionViewer, nil)
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &viewer, "docs", domain.SpacePermissionViewer, false, false)); err != nil {
		t.Fatalf("viewer token read: %v", err)
	}
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &viewer, "docs", domain.SpacePermissionViewer, true, false)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer token write error = %v, want ErrForbidden", err)
	}

	editor := issueSpaceGrantGuardToken(t, f, "acct-editor", domain.SpacePermissionEditor, nil)
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &editor, "docs", domain.SpacePermissionEditor, true, false)); err != nil {
		t.Fatalf("editor token write: %v", err)
	}
	if _, err := f.db.Exec(`UPDATE space_members SET permission = 'viewer' WHERE space_id = ? AND account_id = 'acct-editor'`, f.spaceID); err != nil {
		t.Fatalf("downgrade account ACL: %v", err)
	}
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &editor, "docs", domain.SpacePermissionEditor, true, false)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("downgraded account write error = %v, want ErrForbidden", err)
	}
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &editor, "docs", domain.SpacePermissionViewer, false, false)); err != nil {
		t.Fatalf("downgraded account read: %v", err)
	}
}

func TestGuardSpaceGrantIncludesFutureMountButNotFutureSpace(t *testing.T) {
	f := newGuardFixture(t)
	guard := NewGuard(f.db)
	principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionEditor, nil)

	insertActiveMount(t, f, "mount-future", f.spaceID)
	req := spaceGrantRequest(f, &principal, "future.txt", domain.SpacePermissionEditor, true, false)
	req.Locator.MountID = "mount-future"
	if _, err := guard.Authorize(context.Background(), req); err != nil {
		t.Fatalf("future mount in granted space: %v", err)
	}

	if _, err := f.db.Exec(`
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-future', 'shared', 'Future', 'acct-manager', 'active')
`); err != nil {
		t.Fatalf("insert future space: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO space_members(space_id, account_id, permission) VALUES ('space-future', 'acct-manager', 'manager')`); err != nil {
		t.Fatalf("insert future space membership: %v", err)
	}
	insertActiveMount(t, f, "mount-future-space", "space-future")
	req.Locator.SpaceID = "space-future"
	req.Locator.MountID = "mount-future-space"
	if _, err := guard.Authorize(context.Background(), req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ungranted future space error = %v, want ErrForbidden", err)
	}
}

func TestGuardSpaceGrantDenyExactDescendantSiblingAndRecursiveParent(t *testing.T) {
	f := newGuardFixture(t)
	deny := createDenyDirectory(t, f, "docs/private")
	principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionEditor, []aitoken.FolderDeny{deny})
	guard := NewGuard(f.db)
	ctx := context.Background()

	for _, denied := range []string{"docs/private", "docs/private/report.txt"} {
		if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &principal, denied, domain.SpacePermissionViewer, false, false)); !errors.Is(err, ErrFolderDenied) {
			t.Fatalf("Authorize(%q) error = %v, want ErrFolderDenied", denied, err)
		}
	}
	for _, allowed := range []string{"docs", "docs/private-notes", "docs/public/report.txt"} {
		if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &principal, allowed, domain.SpacePermissionViewer, false, false)); err != nil {
			t.Fatalf("Authorize(%q) error = %v, want success", allowed, err)
		}
	}
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &principal, "docs", domain.SpacePermissionEditor, true, true)); !errors.Is(err, ErrFolderDenied) {
		t.Fatalf("recursive parent error = %v, want ErrFolderDenied", err)
	}
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &principal, "docs/public", domain.SpacePermissionEditor, true, true)); err != nil {
		t.Fatalf("recursive sibling subtree: %v", err)
	}
	if err := os.Symlink(filepath.Join(f.root, "docs"), filepath.Join(f.root, "allowed-alias")); err != nil {
		t.Fatalf("create non-deny symlink: %v", err)
	}
	if _, err := guard.Authorize(ctx, spaceGrantRequest(f, &principal, "allowed-alias", domain.SpacePermissionViewer, false, false)); !errors.Is(err, ErrBoundaryViolation) {
		t.Fatalf("non-deny symlink error = %v, want ErrBoundaryViolation", err)
	}
}

func TestFolderPolicyReusableDirectAndSubtreeChecks(t *testing.T) {
	policy, err := NewFolderPolicy(aitoken.SpaceGrant{
		SpaceID:    "space-1",
		Permission: domain.SpacePermissionViewer,
		FolderDenylist: []aitoken.FolderDeny{{
			MountID:             "mount-1",
			RelativePath:        "docs/private",
			IdentityFingerprint: "dev:1:ino:2",
			Status:              aitoken.FolderDenyStatusActive,
		}},
	}, "mount-1")
	if err != nil {
		t.Fatalf("NewFolderPolicy() error = %v", err)
	}
	if err := policy.CheckPath("docs/private/report.txt"); !errors.Is(err, ErrFolderDenied) {
		t.Fatalf("CheckPath(descendant) error = %v, want ErrFolderDenied", err)
	}
	if err := policy.CheckPath("docs"); err != nil {
		t.Fatalf("CheckPath(parent) error = %v, want success", err)
	}
	if err := policy.CheckSubtree("docs"); !errors.Is(err, ErrFolderDenied) {
		t.Fatalf("CheckSubtree(parent) error = %v, want ErrFolderDenied", err)
	}
	if err := policy.CheckSubtree("docs/public"); err != nil {
		t.Fatalf("CheckSubtree(sibling) error = %v, want success", err)
	}
}

func TestGuardFolderDenyNeedsReviewAndIdentityDriftFailClosed(t *testing.T) {
	t.Run("stored needs_review blocks the whole mount", func(t *testing.T) {
		f := newGuardFixture(t)
		deny := createDenyDirectory(t, f, "docs/private")
		principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionViewer, []aitoken.FolderDeny{deny})
		if _, err := f.db.Exec(`UPDATE ai_token_folder_denies SET status = 'needs_review' WHERE token_id = ?`, principal.TokenID); err != nil {
			t.Fatalf("mark deny needs_review: %v", err)
		}
		_, err := NewGuard(f.db).Authorize(context.Background(), spaceGrantRequest(f, &principal, "docs/public", domain.SpacePermissionViewer, false, false))
		if !errors.Is(err, ErrFolderDenylistReviewRequired) {
			t.Fatalf("Authorize() error = %v, want ErrFolderDenylistReviewRequired", err)
		}
	})

	t.Run("directory replacement is persisted as needs_review", func(t *testing.T) {
		f := newGuardFixture(t)
		deny := createDenyDirectory(t, f, "docs/private")
		principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionViewer, []aitoken.FolderDeny{deny})
		deniedPath := filepath.Join(f.root, "docs", "private")
		if err := os.Rename(deniedPath, deniedPath+"-old"); err != nil {
			t.Fatalf("move original deny directory: %v", err)
		}
		if err := os.Mkdir(deniedPath, 0o755); err != nil {
			t.Fatalf("replace deny directory: %v", err)
		}

		_, err := NewGuard(f.db).Authorize(context.Background(), spaceGrantRequest(f, &principal, "docs/public", domain.SpacePermissionViewer, false, false))
		if !errors.Is(err, ErrFolderDenylistReviewRequired) {
			t.Fatalf("Authorize() error = %v, want ErrFolderDenylistReviewRequired", err)
		}
		var status aitoken.FolderDenyStatus
		if err := f.db.QueryRow(`
SELECT status FROM ai_token_folder_denies
WHERE token_id = ? AND space_id = ? AND mount_id = ? AND relative_path = ?
`, principal.TokenID, f.spaceID, f.mountID, deny.RelativePath).Scan(&status); err != nil {
			t.Fatalf("read deny status: %v", err)
		}
		if status != aitoken.FolderDenyStatusNeedsReview {
			t.Fatalf("deny status = %q, want needs_review", status)
		}
	})

	t.Run("symlink replacement is classified as denylist review", func(t *testing.T) {
		f := newGuardFixture(t)
		deny := createDenyDirectory(t, f, "docs/private")
		principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionViewer, []aitoken.FolderDeny{deny})
		deniedPath := filepath.Join(f.root, "docs", "private")
		movedPath := deniedPath + "-old"
		if err := os.Rename(deniedPath, movedPath); err != nil {
			t.Fatalf("move original deny directory: %v", err)
		}
		if err := os.Symlink(movedPath, deniedPath); err != nil {
			t.Fatalf("replace deny directory with symlink: %v", err)
		}

		_, err := NewGuard(f.db).Authorize(context.Background(), spaceGrantRequest(f, &principal, "docs/private", domain.SpacePermissionViewer, false, false))
		if !errors.Is(err, ErrFolderDenylistReviewRequired) {
			t.Fatalf("Authorize() error = %v, want ErrFolderDenylistReviewRequired", err)
		}
		var status aitoken.FolderDenyStatus
		if err := f.db.QueryRow(`SELECT status FROM ai_token_folder_denies WHERE token_id = ?`, principal.TokenID).Scan(&status); err != nil {
			t.Fatalf("read deny status: %v", err)
		}
		if status != aitoken.FolderDenyStatusNeedsReview {
			t.Fatalf("deny status = %q, want needs_review", status)
		}
	})

	t.Run("database write failure remains diagnosable and fail closed", func(t *testing.T) {
		f := newGuardFixture(t)
		deny := createDenyDirectory(t, f, "docs/private")
		principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionViewer, []aitoken.FolderDeny{deny})
		deniedPath := filepath.Join(f.root, "docs", "private")
		if err := os.Rename(deniedPath, deniedPath+"-old"); err != nil {
			t.Fatalf("move original deny directory: %v", err)
		}
		if err := os.Mkdir(deniedPath, 0o755); err != nil {
			t.Fatalf("replace deny directory: %v", err)
		}
		if _, err := f.db.Exec(`
CREATE TRIGGER fail_folder_deny_review
BEFORE UPDATE OF status ON ai_token_folder_denies
BEGIN
  SELECT RAISE(FAIL, 'forced folder deny update failure');
END
`); err != nil {
			t.Fatalf("create failing update trigger: %v", err)
		}

		_, err := NewGuard(f.db).Authorize(context.Background(), spaceGrantRequest(f, &principal, "docs/public", domain.SpacePermissionViewer, false, false))
		if !errors.Is(err, ErrFolderDenylistReviewRequired) {
			t.Fatalf("Authorize() error = %v, want ErrFolderDenylistReviewRequired", err)
		}
		if !strings.Contains(err.Error(), "forced folder deny update failure") {
			t.Fatalf("Authorize() error = %v, want persisted SQL failure reason", err)
		}
	})

	t.Run("zero updated rows remains diagnosable and fail closed", func(t *testing.T) {
		f := newGuardFixture(t)
		deny := createDenyDirectory(t, f, "docs/private")
		principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionViewer, []aitoken.FolderDeny{deny})
		deniedPath := filepath.Join(f.root, "docs", "private")
		if err := os.Rename(deniedPath, deniedPath+"-old"); err != nil {
			t.Fatalf("move original deny directory: %v", err)
		}
		if err := os.Mkdir(deniedPath, 0o755); err != nil {
			t.Fatalf("replace deny directory: %v", err)
		}
		if _, err := f.db.Exec(`
CREATE TRIGGER ignore_folder_deny_review
BEFORE UPDATE OF status ON ai_token_folder_denies
BEGIN
  SELECT RAISE(IGNORE);
END
`); err != nil {
			t.Fatalf("create ignored update trigger: %v", err)
		}

		_, err := NewGuard(f.db).Authorize(context.Background(), spaceGrantRequest(f, &principal, "docs/public", domain.SpacePermissionViewer, false, false))
		if !errors.Is(err, ErrFolderDenylistReviewRequired) {
			t.Fatalf("Authorize() error = %v, want ErrFolderDenylistReviewRequired", err)
		}
		if !strings.Contains(err.Error(), "affected 0 rows") {
			t.Fatalf("Authorize() error = %v, want zero-row diagnostic", err)
		}
	})
}

func TestGuardAuthorizePairChecksBothSpaceGrantEndpoints(t *testing.T) {
	f := newGuardFixture(t)
	deny := createDenyDirectory(t, f, "docs/private")
	principal := issueSpaceGrantGuardToken(t, f, "acct-manager", domain.SpacePermissionEditor, []aitoken.FolderDeny{deny})
	guard := NewGuard(f.db)
	allowed := spaceGrantRequest(f, &principal, "docs/public.txt", domain.SpacePermissionEditor, true, false)
	denied := spaceGrantRequest(f, &principal, "docs/private/report.txt", domain.SpacePermissionEditor, true, false)

	if _, err := guard.AuthorizePair(context.Background(), denied, allowed); !errors.Is(err, ErrFolderDenied) {
		t.Fatalf("denied source error = %v, want ErrFolderDenied", err)
	}
	if _, err := guard.AuthorizePair(context.Background(), allowed, denied); !errors.Is(err, ErrFolderDenied) {
		t.Fatalf("denied destination error = %v, want ErrFolderDenied", err)
	}
}
