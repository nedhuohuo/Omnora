package fileops

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/store"
)

func openJournalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "fileops.db")})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	db := handle.SQL()
	if _, err := db.Exec(`
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('acct-fileops', 'fileops@example.test', 'File Ops', 'member', 'active');
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-fileops', 'shared', 'File Ops', 'acct-fileops', 'active');
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status)
VALUES ('mount-fileops', 'space-fileops', 'Files', '/tmp/fileops', 'managed', 'read_write', 'active');
`); err != nil {
		t.Fatalf("insert fixture: %v", err)
	}
	return db
}

func TestJournalPrepareInsertsPreparedOperation(t *testing.T) {
	db := openJournalTestDB(t)
	journal := NewJournal(db)
	ctx := context.Background()

	err := journal.Prepare(ctx, OperationSpec{
		ID: "fop_1", Kind: KindRename, SpaceID: "space-fileops", MountID: "mount-fileops",
		SourcePath: "a.txt", DestinationPath: "b.txt",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	op, err := journal.Get(ctx, "fop_1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if op.Status != StatusPrepared || op.Kind != KindRename || op.SourcePath != "a.txt" || op.DestinationPath != "b.txt" {
		t.Fatalf("Get() = %#v, want prepared same_mount_rename a.txt -> b.txt", op)
	}
}

func TestJournalPrepareRollsBackWhenSideEffectFails(t *testing.T) {
	db := openJournalTestDB(t)
	journal := NewJournal(db)
	ctx := context.Background()
	sideEffectErr := errors.New("side effect failed")

	err := journal.Prepare(ctx, OperationSpec{ID: "fop_2", Kind: KindDelete, SpaceID: "space-fileops", MountID: "mount-fileops", SourcePath: "a.txt"},
		func(ctx context.Context, tx *sql.Tx) error { return sideEffectErr },
	)
	if !errors.Is(err, sideEffectErr) {
		t.Fatalf("Prepare() error = %v, want side effect error", err)
	}
	if _, err := journal.Get(ctx, "fop_2"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("Get() error = %v, want not found after rollback", err)
	}
}

func TestJournalCompleteRequiresPreparedStatus(t *testing.T) {
	db := openJournalTestDB(t)
	journal := NewJournal(db)
	ctx := context.Background()
	if err := journal.Prepare(ctx, OperationSpec{ID: "fop_3", Kind: KindDelete, SpaceID: "space-fileops", MountID: "mount-fileops", SourcePath: "a.txt"}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := journal.Complete(ctx, "fop_3"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := journal.Complete(ctx, "fop_3"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("second Complete() error = %v, want not found (already advanced)", err)
	}
	op, err := journal.Get(ctx, "fop_3")
	if err != nil || op.Status != StatusCompleted {
		t.Fatalf("Get() = %#v, error = %v, want completed", op, err)
	}
}

func TestJournalRequireRecoveryRecordsLastError(t *testing.T) {
	db := openJournalTestDB(t)
	journal := NewJournal(db)
	ctx := context.Background()
	if err := journal.Prepare(ctx, OperationSpec{ID: "fop_4", Kind: KindTrash, SpaceID: "space-fileops", MountID: "mount-fileops", SourcePath: "a.txt"}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	cause := errors.New("disk full")
	if err := journal.RequireRecovery(ctx, "fop_4", cause); err != nil {
		t.Fatalf("RequireRecovery() error = %v", err)
	}
	op, err := journal.Get(ctx, "fop_4")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if op.Status != StatusRecoveryRequired {
		t.Fatalf("status = %q, want recovery_required", op.Status)
	}
	if !strings.Contains(op.LastError, "disk full") {
		t.Fatalf("last_error = %q, want to contain cause", op.LastError)
	}
	// A recovered-from operation cannot also be completed: exactly one
	// terminal-ish transition may win the prepared -> * race.
	if err := journal.Complete(ctx, "fop_4"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("Complete() after recovery error = %v, want not found", err)
	}
}

func TestJournalGetUnknownOperation(t *testing.T) {
	db := openJournalTestDB(t)
	journal := NewJournal(db)
	if _, err := journal.Get(context.Background(), "does-not-exist"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("Get() error = %v, want not found", err)
	}
}
