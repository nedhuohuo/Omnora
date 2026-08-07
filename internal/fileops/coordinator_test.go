package fileops

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"omnora/internal/domain"
	"omnora/internal/files"
)

type recordingInvalidator struct {
	calls []string
	// sawSourceExists records, per call, whether the source path still
	// existed on disk at invalidation time. This proves Prepare's side
	// effects run before any filesystem mutation.
	sawSourceExists []bool
	root            string
}

func (r *recordingInvalidator) RevokePathTx(_ context.Context, _ *sql.Tx, spaceID, mountID, relativePath string) error {
	r.calls = append(r.calls, spaceID+":"+mountID+":"+relativePath)
	_, err := os.Stat(filepath.Join(r.root, relativePath))
	r.sawSourceExists = append(r.sawSourceExists, err == nil)
	return nil
}

func newCoordinatorFixture(t *testing.T) (*Coordinator, *Journal, *recordingInvalidator, files.Mount) {
	t.Helper()
	db := openJournalTestDB(t)
	root := t.TempDir()
	invalidator := &recordingInvalidator{root: root}
	coordinator := NewCoordinator(db, WithShareInvalidator(invalidator))
	mount := files.Mount{Root: root, Mode: domain.MountModeReadWrite, Kind: "managed"}
	return coordinator, NewJournal(db), invalidator, mount
}

func writeFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func noopAudit(context.Context, *sql.Tx) error { return nil }

func TestCoordinatorRenameInvalidatesBeforeIOAndCompletesOperation(t *testing.T) {
	coordinator, journal, invalidator, mount := newCoordinatorFixture(t)
	writeFixtureFile(t, mount.Root, "a.txt", "a")

	renamed, err := coordinator.Rename(context.Background(), mount, "space-fileops", "mount-fileops", "a.txt", "b.txt", noopAudit)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if renamed != "b.txt" {
		t.Fatalf("Rename() = %q, want b.txt", renamed)
	}
	if _, err := os.Stat(filepath.Join(mount.Root, "b.txt")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if len(invalidator.calls) != 1 || invalidator.calls[0] != "space-fileops:mount-fileops:a.txt" {
		t.Fatalf("invalidator calls = %#v, want one call for source path", invalidator.calls)
	}
	if len(invalidator.sawSourceExists) != 1 || !invalidator.sawSourceExists[0] {
		t.Fatal("share invalidation observed the source file already gone; it must run before the filesystem rename")
	}
	ops := listOperations(t, journal)
	if len(ops) != 1 || ops[0].Status != StatusCompleted || ops[0].Kind != KindRename {
		t.Fatalf("operations = %#v, want one completed same_mount_rename", ops)
	}
}

func TestCoordinatorAuditFailureLeavesFilesystemAndOperationUntouched(t *testing.T) {
	coordinator, journal, _, mount := newCoordinatorFixture(t)
	writeFixtureFile(t, mount.Root, "source.txt", "hello")
	auditErr := errors.New("audit write failed")

	_, err := coordinator.Rename(context.Background(), mount, "space-fileops", "mount-fileops", "source.txt", "renamed.txt",
		func(context.Context, *sql.Tx) error { return auditErr })
	if !errors.Is(err, auditErr) {
		t.Fatalf("Rename() error = %v, want audit error", err)
	}
	if _, err := os.Stat(filepath.Join(mount.Root, "source.txt")); err != nil {
		t.Fatalf("source file was moved despite audit failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mount.Root, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("destination file unexpectedly exists after audit failure")
	}
	// The share invalidation side effect ran against the shared tx before the
	// audit side effect failed; since Prepare rolls the whole tx back, a real
	// (DB-backed) invalidator's writes are undone even though this in-memory
	// stub already recorded the call. What must hold is that no operation
	// row survives the rollback and the filesystem was never touched.
	if ops := listOperations(t, journal); len(ops) != 0 {
		t.Fatalf("operations = %#v, want no durable row after rollback", ops)
	}
}

func TestCoordinatorDeleteMarksRecoveryRequiredOnFilesystemFailure(t *testing.T) {
	coordinator, journal, _, mount := newCoordinatorFixture(t)
	// No file created at "missing.txt": the durable prepare succeeds but the
	// filesystem delete then fails, which must not be silently dropped.
	err := coordinator.Delete(context.Background(), mount, "space-fileops", "mount-fileops", "missing.txt", noopAudit)
	if err == nil {
		t.Fatal("Delete() unexpectedly succeeded against a missing file")
	}
	ops := listOperations(t, journal)
	if len(ops) != 1 || ops[0].Status != StatusRecoveryRequired || ops[0].Kind != KindDelete {
		t.Fatalf("operations = %#v, want one recovery_required delete", ops)
	}
	if ops[0].LastError == "" {
		t.Fatal("recovery_required operation has no last_error recorded")
	}
}

func TestCoordinatorTrashCompletesAndRestoreSkipsInvalidation(t *testing.T) {
	coordinator, journal, invalidator, mount := newCoordinatorFixture(t)
	writeFixtureFile(t, mount.Root, "doc.txt", "trash me")

	item, err := coordinator.Trash(context.Background(), mount, "space-fileops", "mount-fileops", "doc.txt", noopAudit)
	if err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	if item.ID == "" || item.OriginalPath != "doc.txt" {
		t.Fatalf("Trash() = %#v, want populated trash item", item)
	}
	if len(invalidator.calls) != 1 {
		t.Fatalf("invalidator calls = %#v, want exactly one for trash", invalidator.calls)
	}
	ops := listOperations(t, journal)
	if len(ops) != 1 || ops[0].Status != StatusCompleted || ops[0].Kind != KindTrash {
		t.Fatalf("operations after trash = %#v, want one completed trash", ops)
	}

	restored, err := coordinator.RestoreTrash(context.Background(), mount, "space-fileops", "mount-fileops", item.ID, noopAudit)
	if err != nil {
		t.Fatalf("RestoreTrash() error = %v", err)
	}
	if restored != "doc.txt" {
		t.Fatalf("RestoreTrash() = %q, want doc.txt", restored)
	}
	if len(invalidator.calls) != 1 {
		t.Fatalf("invalidator calls after restore = %#v, want unchanged (restore never invalidates)", invalidator.calls)
	}
	ops = listOperations(t, journal)
	if len(ops) != 2 {
		t.Fatalf("operations after restore = %#v, want trash + restore rows", ops)
	}
	var restoreOp Operation
	for _, op := range ops {
		if op.Kind == KindTrashRestore {
			restoreOp = op
		}
	}
	if restoreOp.Status != StatusCompleted {
		t.Fatalf("restore operation = %#v, want completed", restoreOp)
	}
}

func TestCoordinatorWithoutJournalReturnsConfigurationError(t *testing.T) {
	var coordinator Coordinator
	if _, err := coordinator.Rename(context.Background(), files.Mount{}, "s", "m", "a", "b", noopAudit); !errors.Is(err, ErrJournalNotConfigured) {
		t.Fatalf("Rename() error = %v, want journal not configured", err)
	}
	if err := coordinator.Delete(context.Background(), files.Mount{}, "s", "m", "a", noopAudit); !errors.Is(err, ErrJournalNotConfigured) {
		t.Fatalf("Delete() error = %v, want journal not configured", err)
	}
}

func listOperations(t *testing.T, journal *Journal) []Operation {
	t.Helper()
	rows, err := journal.db.Query(`SELECT id FROM file_operations ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("query operations: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	ops := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, err := journal.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", id, err)
		}
		ops = append(ops, op)
	}
	return ops
}
