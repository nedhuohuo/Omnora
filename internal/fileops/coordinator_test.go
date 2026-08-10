package fileops

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/store"
)

type recordingInvalidator struct {
	mountID string
	path    string
}

func (r *recordingInvalidator) RevokePathTx(_ context.Context, _ *sql.Tx, mountID, storageRelativePath string) error {
	r.mountID, r.path = mountID, storageRelativePath
	return nil
}

func fileopsDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "fileops.db")})
	if err != nil {
		t.Fatalf("open target database: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle.SQL()
}

func TestJournalPersistsMountStorageCoordinates(t *testing.T) {
	db := fileopsDB(t)
	spec := OperationSpec{
		ID: "fop-target", Kind: KindCrossMountMove,
		SourceMountID: "personal-default", SourceStorageRelativePath: "acct-1/docs/a.txt",
		DestinationMountID: "personal-default", DestinationStorageRelativePath: "acct-1/archive",
	}
	journal := NewJournal(db)
	if err := journal.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	operation, err := journal.Get(context.Background(), spec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if operation.SourcePath != "acct-1/docs/a.txt" || operation.DestinationPath != "acct-1/archive" {
		t.Fatalf("operation = %#v", operation)
	}
	if err := journal.Complete(context.Background(), spec.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestCoordinatorInvalidatesByMountAndStoragePathBeforeRename(t *testing.T) {
	db := fileopsDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "acct-1"), 0o700); err != nil {
		t.Fatalf("create account directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "acct-1", "before.txt"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	invalidator := &recordingInvalidator{}
	coordinator := NewCoordinator(db, WithShareInvalidator(invalidator), WithIDGenerator(func() string { return "fop-rename" }))
	mount := files.Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}
	if _, err := coordinator.Rename(context.Background(), mount, "personal-default", "acct-1/before.txt", "acct-1/after.txt", nil); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if invalidator.mountID != "personal-default" || invalidator.path != "acct-1/before.txt" {
		t.Fatalf("invalidation = %q %q", invalidator.mountID, invalidator.path)
	}
	operation, err := NewJournal(db).Get(context.Background(), "fop-rename")
	if err != nil || operation.Status != StatusCompleted {
		t.Fatalf("operation = %#v, err=%v", operation, err)
	}
}

func TestCoordinatorDurablyTracksTrashPurgeAndEmpty(t *testing.T) {
	db := fileopsDB(t)
	root := t.TempDir()
	mount := files.Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}
	fileService := files.NewService()
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	first, err := fileService.SoftDelete(mount, "one.txt")
	if err != nil {
		t.Fatalf("soft delete first: %v", err)
	}
	if _, err := fileService.SoftDelete(mount, "two.txt"); err != nil {
		t.Fatalf("soft delete second: %v", err)
	}
	ids := []string{"fop-purge", "fop-empty", "fop-purge-failed"}
	coordinator := NewCoordinator(db, WithIDGenerator(func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}))
	if err := coordinator.PurgeTrash(context.Background(), mount, "personal-default", "acct-1/.omnora/trash/"+first.ID, first.ID, nil); err != nil {
		t.Fatalf("PurgeTrash: %v", err)
	}
	removed, err := coordinator.EmptyTrash(context.Background(), mount, "personal-default", "acct-1/.omnora/trash", nil)
	if err != nil || removed != 1 {
		t.Fatalf("EmptyTrash removed=%d err=%v", removed, err)
	}
	if err := coordinator.PurgeTrash(context.Background(), mount, "personal-default", "acct-1/.omnora/trash/missing", "trash_missing", nil); err == nil {
		t.Fatal("PurgeTrash missing item succeeded")
	}
	for _, test := range []struct {
		id     string
		kind   Kind
		status Status
	}{
		{id: "fop-purge", kind: KindTrashPurge, status: StatusCompleted},
		{id: "fop-empty", kind: KindTrashEmpty, status: StatusCompleted},
		{id: "fop-purge-failed", kind: KindTrashPurge, status: StatusRecoveryRequired},
	} {
		op, err := NewJournal(db).Get(context.Background(), test.id)
		if err != nil || op.Kind != test.kind || op.Status != test.status {
			t.Fatalf("operation %s = %#v, err=%v", test.id, op, err)
		}
	}
}
