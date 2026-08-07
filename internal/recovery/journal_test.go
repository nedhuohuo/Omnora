package recovery

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRehydrateFromJournalRestoresRequestLostByDatabaseReplacement simulates
// the offline recovery worker replacing the live SQLite file with an older
// backup snapshot after BeginRestore already committed a restore request.
// That replacement wipes out the restore_requests/recovery_control rows
// BeginRestore wrote, because the backup predates the request. The journal
// written at BeginRestore must be enough to recreate them so
// ApplyRestoreEffects can still run.
func TestRehydrateFromJournalRestoresRequestLostByDatabaseReplacement(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "omnora.db.recovery-journal.json")
	coordinator := NewCoordinator(db.SQL(), WithJournalPath(journalPath), WithClock(func() time.Time {
		return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	}))

	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status, password_hash)
VALUES ('admin', 'admin@example.com', 'Admin', 'admin', 'active', 'hash')
`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO backups(id, status, path, created_by, created_at, notes)
VALUES ('backup-1', 'completed', '/tmp/backup.db', 'admin', CURRENT_TIMESTAMP, 'test')
`); err != nil {
		t.Fatalf("insert backup: %v", err)
	}

	request, err := coordinator.BeginRestore(ctx, BeginRestoreRequest{BackupID: "backup-1", ActorAccountID: "admin"})
	if err != nil {
		t.Fatalf("begin restore: %v", err)
	}

	if _, err := ReadJournal(journalPath); err != nil {
		t.Fatalf("journal was not written at BeginRestore: %v", err)
	}

	// Simulate the offline worker replacing the live database with a backup
	// snapshot taken before the restore request existed: the request row and
	// the control-plane pointer to it disappear, but the sidecar journal
	// survives because it lives outside the database file.
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM restore_requests WHERE id = ?`, request.ID); err != nil {
		t.Fatalf("simulate lost restore_requests row: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
UPDATE recovery_control
SET state = 'normal', ready = 0, request_id = NULL, staging_path = NULL,
    reason_code = NULL, cleanup_pending = 0, requested_at = NULL, completed_at = NULL
WHERE id = 1
`); err != nil {
		t.Fatalf("simulate reset recovery_control row: %v", err)
	}

	changed, err := coordinator.RehydrateFromJournal(ctx)
	if err != nil {
		t.Fatalf("rehydrate from journal: %v", err)
	}
	if !changed {
		t.Fatal("rehydrate from journal reported no change, want the lost rows restored")
	}

	control, err := coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if control.State != StatePreparing || control.RequestID != request.ID {
		t.Fatalf("control after rehydrate = %#v, want preparing/%s", control, request.ID)
	}

	restored, err := coordinator.Request(ctx, request.ID)
	if err != nil {
		t.Fatalf("read restored request: %v", err)
	}
	if restored.State != StatePreparing || restored.BackupID != "backup-1" {
		t.Fatalf("restored request = %#v", restored)
	}

	// With the request rehydrated, the rest of the offline recovery flow
	// must be able to continue exactly as if the database had never been
	// replaced underneath it.
	if _, err := coordinator.MarkRestoring(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("mark restoring after rehydrate: %v", err)
	}
	if _, err := coordinator.ApplyRestoreEffects(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("apply restore effects after rehydrate: %v", err)
	}

	// A second rehydrate call must be a safe no-op once the database already
	// has (and has progressed past) the journaled request.
	changedAgain, err := coordinator.RehydrateFromJournal(ctx)
	if err != nil {
		t.Fatalf("second rehydrate from journal: %v", err)
	}
	if changedAgain {
		t.Fatal("second rehydrate from journal reported a change, want no-op")
	}
	control, err = coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read control after second rehydrate: %v", err)
	}
	if control.State != StateFinalizeRequired {
		t.Fatalf("control after second rehydrate = %#v, want finalize_required preserved", control)
	}
}

// TestRehydrateFromJournalNoopWithoutJournalPath ensures a coordinator that
// was never given a journal path (e.g. the legacy in-process test/CLI path)
// behaves as a no-op rather than failing.
func TestRehydrateFromJournalNoopWithoutJournalPath(t *testing.T) {
	db := openRecoveryTestDB(t)
	coordinator := NewCoordinator(db.SQL())
	changed, err := coordinator.RehydrateFromJournal(context.Background())
	if err != nil {
		t.Fatalf("rehydrate without journal path: %v", err)
	}
	if changed {
		t.Fatal("rehydrate without journal path reported a change")
	}
}

// TestRehydrateFromJournalNoopWithoutJournalFile ensures a missing journal
// file (no restore ever started) is not treated as an error.
func TestRehydrateFromJournalNoopWithoutJournalFile(t *testing.T) {
	db := openRecoveryTestDB(t)
	journalPath := filepath.Join(t.TempDir(), "missing.json")
	coordinator := NewCoordinator(db.SQL(), WithJournalPath(journalPath))
	changed, err := coordinator.RehydrateFromJournal(context.Background())
	if err != nil {
		t.Fatalf("rehydrate with missing journal file: %v", err)
	}
	if changed {
		t.Fatal("rehydrate with missing journal file reported a change")
	}
}
