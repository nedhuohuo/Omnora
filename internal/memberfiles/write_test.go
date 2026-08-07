package memberfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/access"
	"omnora/internal/catalog"
	"omnora/internal/domain"
	"omnora/internal/files"
)

func sha256Hex(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

type mutationInvalidator struct {
	paths []string
	check func(string)
}

func (m *mutationInvalidator) RevokePath(_ context.Context, spaceID, mountID, relativePath string) error {
	if m.check != nil {
		m.check(relativePath)
	}
	m.paths = append(m.paths, spaceID+":"+mountID+":"+relativePath)
	return nil
}

func TestMutationsEnforceLiveGuardAndInvalidateOnlyAfterSuccess(t *testing.T) {
	f := newReadFixture(t)
	invalidator := &mutationInvalidator{check: func(relativePath string) {
		if _, err := os.Stat(filepath.Join(f.root, relativePath)); err == nil {
			t.Fatalf("share invalidation ran before mutation completed for %q", relativePath)
		}
	}}
	s := NewService(f.db, access.NewGuard(f.db), catalog.NewService(f.db), WithShareInvalidator(invalidator))
	ctx := context.Background()

	created, err := s.CreateDirectory(ctx, f.session(), f.locator("."), "new")
	if err != nil || created.RelativePath != "new" {
		t.Fatalf("CreateDirectory() = %#v, error = %v", created, err)
	}
	if _, err := s.CreateDirectory(ctx, f.session(), f.locator("."), ".omnora"); err == nil {
		t.Fatal("CreateDirectory() unexpectedly allowed reserved namespace")
	}
	writeReadFile(t, filepath.Join(f.root, "source.txt"), "source")
	rename, err := s.Rename(ctx, f.session(), f.locator("source.txt"), "renamed.txt")
	if err != nil || rename.RelativePath != "renamed.txt" {
		t.Fatalf("Rename() = %#v, error = %v", rename, err)
	}
	if len(invalidator.paths) != 1 || !strings.HasSuffix(invalidator.paths[0], ":source.txt") {
		t.Fatalf("invalidations = %#v, want source path after rename", invalidator.paths)
	}
	writeReadFile(t, filepath.Join(f.root, "copy.txt"), "copy")
	if _, err := s.Copy(ctx, f.session(), f.locator("copy.txt"), f.locator(".")); err != nil {
		// Destination basename conflicts are expected when copying into the same
		// directory; the source must remain and no share invalidation is emitted.
		if !errors.Is(err, files.ErrInvalidMount) {
			t.Fatalf("Copy() error = %v, want conflict", err)
		}
	}
	if len(invalidator.paths) != 1 {
		t.Fatalf("copy invalidated source: %#v", invalidator.paths)
	}

	if _, err := s.Rename(ctx, f.session(), f.locator("renamed.txt"), "new/renamed.txt"); err != nil {
		t.Fatalf("Rename() into new directory error = %v", err)
	}
	if _, err := s.Rename(ctx, f.session(), f.locator("new/renamed.txt"), "new/renamed.txt"); err == nil {
		t.Fatal("Rename() unexpectedly allowed overwrite")
	}
	readonly := f.session()
	if _, err := f.db.Exec(`UPDATE mounts SET mode = ? WHERE id = ?`, domain.MountModeReadOnly, f.mountID); err != nil {
		t.Fatalf("set readonly: %v", err)
	}
	if _, err := s.CreateDirectory(ctx, readonly, f.locator("."), "nope"); !errors.Is(err, access.ErrReadonlyMount) {
		t.Fatalf("CreateDirectory(read-only) error = %v, want readonly", err)
	}
}

func TestUploadLifecycleChecksumRaceAndCancel(t *testing.T) {
	f := newReadFixture(t)
	s := NewService(f.db, access.NewGuard(f.db), catalog.NewService(f.db))
	ctx := context.Background()
	checksum := sha256Hex("hello")
	upload, err := s.PrepareUpload(ctx, f.session(), UploadRequest{
		Locator:      f.locator("incoming.txt"),
		ExpectedSize: 5,
		Checksum:     checksum,
	})
	if err != nil {
		t.Fatalf("PrepareUpload() error = %v", err)
	}
	if upload.ID == "" || upload.TargetPath != "incoming.txt" {
		t.Fatalf("upload = %#v", upload)
	}
	if _, err := s.UploadStatus(ctx, f.session(), upload.ID); err != nil {
		t.Fatalf("UploadStatus() error = %v", err)
	}
	if err := s.WriteUploadPart(ctx, f.session(), upload.ID, 1, strings.NewReader("hello")); err != nil {
		t.Fatalf("WriteUploadPart() error = %v", err)
	}
	completed, err := s.CompleteUpload(ctx, f.session(), upload.ID)
	if err != nil || completed.RelativePath != "incoming.txt" {
		t.Fatalf("CompleteUpload() = %#v, error = %v", completed, err)
	}
	body, err := os.ReadFile(filepath.Join(f.root, "incoming.txt"))
	if err != nil || string(body) != "hello" {
		t.Fatalf("completed body = %q, error = %v", body, err)
	}

	bad, err := s.PrepareUpload(ctx, f.session(), UploadRequest{Locator: f.locator("bad.txt"), ExpectedSize: 4, Checksum: sha256Hex("good")})
	if err != nil {
		t.Fatalf("PrepareUpload(bad) error = %v", err)
	}
	if err := s.WriteUploadPart(ctx, f.session(), bad.ID, 1, strings.NewReader("nope")); err != nil {
		t.Fatalf("WriteUploadPart(bad) error = %v", err)
	}
	if _, err := s.CompleteUpload(ctx, f.session(), bad.ID); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("CompleteUpload(bad) error = %v, want checksum mismatch", err)
	}
	if err := s.CancelUpload(ctx, f.session(), bad.ID); err != nil {
		t.Fatalf("CancelUpload() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "bad.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled target exists: %v", err)
	}

	raced, err := s.PrepareUpload(ctx, f.session(), UploadRequest{Locator: f.locator("race.txt"), ExpectedSize: 4})
	if err != nil {
		t.Fatalf("PrepareUpload(race) error = %v", err)
	}
	if err := s.WriteUploadPart(ctx, f.session(), raced.ID, 1, strings.NewReader("data")); err != nil {
		t.Fatalf("WriteUploadPart(race) error = %v", err)
	}
	writeReadFile(t, filepath.Join(f.root, "race.txt"), "keep")
	if _, err := s.CompleteUpload(ctx, f.session(), raced.ID); !errors.Is(err, files.ErrInvalidMount) && !errors.Is(err, ErrUploadConflict) {
		t.Fatalf("CompleteUpload(race) error = %v, want conflict", err)
	}
	body, _ = os.ReadFile(filepath.Join(f.root, "race.txt"))
	if string(body) != "keep" {
		t.Fatalf("race target changed to %q", body)
	}
}

func TestUploadStatusRejectsSessionAfterCredentialGenerationBump(t *testing.T) {
	f := newReadFixture(t)
	s := NewService(f.db, access.NewGuard(f.db), catalog.NewService(f.db))
	ctx := context.Background()
	upload, err := s.PrepareUpload(ctx, f.session(), UploadRequest{
		Locator:      f.locator("incoming.txt"),
		ExpectedSize: 5,
		Checksum:     sha256Hex("hello"),
	})
	if err != nil {
		t.Fatalf("PrepareUpload() error = %v", err)
	}
	if _, err := s.UploadStatus(ctx, f.session(), upload.ID); err != nil {
		t.Fatalf("UploadStatus() before generation bump error = %v", err)
	}

	// Simulate an account-wide security event bumping the credential epoch:
	// the in-flight upload session must stop resolving even though its row
	// was never touched, the same way an expired lease already does.
	if _, err := f.db.Exec(`UPDATE system_state SET value = '2' WHERE key = 'credential_generation'`); err != nil {
		t.Fatalf("bump credential_generation: %v", err)
	}

	if _, err := s.UploadStatus(ctx, f.session(), upload.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("UploadStatus() after generation bump error = %v, want ErrUploadNotFound", err)
	}
	if err := s.WriteUploadPart(ctx, f.session(), upload.ID, 1, strings.NewReader("hello")); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("WriteUploadPart() after generation bump error = %v, want ErrUploadNotFound", err)
	}
}

func TestCopyMoveTrashAndPermanentDelete(t *testing.T) {
	f := newReadFixture(t)
	s := NewService(f.db, access.NewGuard(f.db), catalog.NewService(f.db))
	ctx := context.Background()
	writeReadFile(t, filepath.Join(f.root, "move.txt"), "move")
	if _, err := s.CrossMountCopy(ctx, f.session(), f.locator("move.txt"), f.locator(".")); !errors.Is(err, ErrCrossMountSameMount) {
		t.Fatalf("CrossMountCopy(same mount) error = %v, want same-mount rejection", err)
	}
	if _, err := s.Copy(ctx, f.session(), f.locator("move.txt"), f.locator("docs2")); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if _, err := s.Move(ctx, f.session(), f.locator("move.txt"), f.locator("docs")); err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	trash, err := s.Trash(ctx, f.session(), f.locator("docs/move.txt"))
	if err != nil || trash.TrashID == "" {
		t.Fatalf("Trash() = %#v, error = %v", trash, err)
	}
	items, err := s.ListTrash(ctx, f.session(), f.locator("."))
	if err != nil || len(items.Items) != 1 {
		t.Fatalf("ListTrash() = %#v, error = %v", items, err)
	}
	if _, err := s.RestoreTrash(ctx, f.session(), f.locator("."), trash.TrashID); err != nil {
		t.Fatalf("RestoreTrash() error = %v", err)
	}
	if _, err := s.DeletePermanently(ctx, f.session(), f.locator("docs/move.txt")); err != nil {
		t.Fatalf("DeletePermanently() error = %v", err)
	}
	if _, err := s.Trash(ctx, f.session(), f.locator(".omnora")); err == nil {
		t.Fatal("Trash() unexpectedly allowed reserved namespace")
	}
}

func TestListTrashAllowsReadOnlyManagedMount(t *testing.T) {
	f := newReadFixture(t)
	writeReadFile(t, filepath.Join(f.root, "readonly-trash.txt"), "keep")
	mount := files.Mount{Root: f.root, Mode: domain.MountModeReadWrite, Kind: "managed"}
	if _, err := files.NewService().SoftDelete(mount, "readonly-trash.txt"); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}
	if _, err := f.db.Exec(`UPDATE mounts SET mode = ? WHERE id = ?`, domain.MountModeReadOnly, f.mountID); err != nil {
		t.Fatalf("set read-only mode: %v", err)
	}
	s := f.service()
	result, err := s.ListTrash(context.Background(), f.session(), f.locator("."))
	if err != nil || result.TotalCount != 1 {
		t.Fatalf("ListTrash(read-only) = %#v, error = %v", result, err)
	}
	if _, err := f.db.Exec(`UPDATE space_members SET permission = ? WHERE space_id = ? AND account_id = ?`, domain.SpacePermissionViewer, f.spaceID, "acct-memberfiles"); err != nil {
		t.Fatalf("set viewer ACL: %v", err)
	}
	if _, err := s.ListTrash(context.Background(), f.session(), f.locator(".")); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("ListTrash(viewer) error = %v, want forbidden", err)
	}
	if result, err := s.ListTrashViewer(context.Background(), f.session(), f.locator(".")); err != nil || result.TotalCount != 1 {
		t.Fatalf("ListTrashViewer(viewer) = %#v, error = %v", result, err)
	}
	if _, err := f.db.Exec(`UPDATE space_members SET permission = ? WHERE space_id = ? AND account_id = ?`, domain.SpacePermissionEditor, f.spaceID, "acct-memberfiles"); err != nil {
		t.Fatalf("restore editor ACL: %v", err)
	}
	if _, err := s.Trash(context.Background(), f.session(), f.locator("small.txt")); !errors.Is(err, access.ErrReadonlyMount) {
		t.Fatalf("Trash(read-only) error = %v, want readonly", err)
	}
}
