package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/offlinemigration"
	"omnora/internal/recovery"
	"omnora/internal/store"
)

func TestAccountMountMigrationRequestAddsMandatoryInvariants(t *testing.T) {
	request := accountMountMigrationRequest(offlinemigration.MigrationRequest{})
	if len(request.Invariants) == 0 {
		t.Fatal("account-mount migration request has no domain invariants")
	}
}

func TestDoMigrateAccountMountSafelyExitsWhenProductionHasNoOfflineMigration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := offlinemigration.MigrationRequest{
		DBPath: filepath.Join(root, "data", "omnora.db"), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), ManagedDir: filepath.Join(root, "managed"), RollbackRoot: filepath.Join(root, "rollback"),
	}
	for _, dir := range []string{request.ConfigDir, request.DataDir, request.ManagedDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".omnora-instance-id"), []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: request.DBPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := doMigrateAccountMount(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoPendingMigration {
		t.Fatalf("result = %#v, want safe no-op", result)
	}
}

// TestDoRestoreAndDoFinalizeEndToEnd exercises the full offline recovery
// path: a live database with a restore request in 'preparing' (as BeginRestore
// leaves it), a completed backup snapshot, doRestore replacing the live
// database and driving the coordinator to normal_pending_bootstrap, and
// doFinalize returning it to normal.
func TestDoRestoreAndDoFinalizeEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnora.db")
	backupPath := filepath.Join(dir, "backup.db")
	journalPath := recovery.DefaultJournalPath(dbPath)

	liveDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: dbPath})
	if err != nil {
		t.Fatalf("open live database: %v", err)
	}
	if _, err := liveDB.SQL().ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status, password_hash)
VALUES ('admin', 'admin@example.com', 'Admin', 'admin', 'active', 'hash')
`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	// The backup itself must be a valid, self-contained SQLite snapshot; take
	// an online backup of the current (pre-restore) live database so it has
	// the same schema and passes integrity/foreign-key validation.
	if err := liveDB.BackupTo(ctx, backupPath); err != nil {
		t.Fatalf("create backup snapshot: %v", err)
	}
	backupContents, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup snapshot: %v", err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("stat backup snapshot: %v", err)
	}
	var backupSchemaVersion int64
	if err := liveDB.SQL().QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&backupSchemaVersion); err != nil {
		t.Fatalf("read backup schema version: %v", err)
	}
	backupSHA256 := sha256.Sum256(backupContents)
	canonicalBackupPath, err := filepath.Abs(backupPath)
	if err != nil {
		t.Fatalf("resolve backup canonical path: %v", err)
	}
	if _, err := liveDB.SQL().ExecContext(ctx, `
INSERT INTO backups(id, status, path, created_by, created_at, completed_at, notes, sha256, size_bytes, canonical_path, schema_version)
VALUES ('backup-1', 'completed', ?, 'admin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'test', ?, ?, ?, ?)
`, backupPath, hex.EncodeToString(backupSHA256[:]), backupInfo.Size(), canonicalBackupPath, backupSchemaVersion); err != nil {
		t.Fatalf("insert backup record: %v", err)
	}

	coordinator := recovery.NewCoordinator(liveDB.SQL(), recovery.WithJournalPath(journalPath))
	request, err := coordinator.BeginRestore(ctx, recovery.BeginRestoreRequest{
		BackupID:       "backup-1",
		ActorAccountID: "admin",
	})
	if err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	if err := liveDB.Close(); err != nil {
		t.Fatalf("close live database (simulating the controlled process shutdown): %v", err)
	}

	// An empty actor (the CLI's default) must not violate the
	// audit_events.actor_account_id foreign key: it should record an
	// unattributed system actor rather than a fabricated account id.
	if err := doRestore(ctx, restoreParams{
		dbPath:      dbPath,
		backupPath:  backupPath,
		requestID:   request.ID,
		journalPath: journalPath,
		actor:       "",
	}); err != nil {
		t.Fatalf("doRestore failed: %v", err)
	}

	if _, err := os.Stat(dbPath + ".pre-restore-safe-snapshot-" + request.ID + ".db"); err != nil {
		t.Fatalf("expected pre-restore safe snapshot on disk: %v", err)
	}
	if _, err := os.Stat(dbPath + ".restore-staging-" + request.ID + ".db"); !os.IsNotExist(err) {
		t.Fatalf("expected staging file to be cleaned up, stat err = %v", err)
	}

	replacedDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: dbPath})
	if err != nil {
		t.Fatalf("reopen replaced live database: %v", err)
	}
	defer replacedDB.Close()

	control, err := recovery.NewCoordinator(replacedDB.SQL()).Control(ctx)
	if err != nil {
		t.Fatalf("read control after restore: %v", err)
	}
	if control.State != recovery.StateNormalPendingBootstrap {
		t.Fatalf("control.State after doRestore = %q, want %q", control.State, recovery.StateNormalPendingBootstrap)
	}

	var revokedSessions int
	if err := replacedDB.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE password_reset_required = 1`).Scan(&revokedSessions); err != nil {
		t.Fatalf("count accounts requiring password reset: %v", err)
	}
	if revokedSessions != 1 {
		t.Fatalf("accounts requiring password reset = %d, want 1 (ApplyRestoreEffects must have run)", revokedSessions)
	}
	if err := replacedDB.Close(); err != nil {
		t.Fatalf("close replaced database before finalize: %v", err)
	}

	finalState, ferr := doFinalize(ctx, dbPath, request.ID, journalPath, "")
	if ferr != nil {
		t.Fatalf("doFinalize failed: %v", ferr)
	}
	if finalState != recovery.StateNormal {
		t.Fatalf("doFinalize final state = %q, want %q", finalState, recovery.StateNormal)
	}

	finalDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: dbPath})
	if err != nil {
		t.Fatalf("reopen database after finalize: %v", err)
	}
	defer finalDB.Close()
	control, err = recovery.NewCoordinator(finalDB.SQL()).Control(ctx)
	if err != nil {
		t.Fatalf("read control after finalize: %v", err)
	}
	if control.State != recovery.StateNormal {
		t.Fatalf("control.State after doFinalize = %q, want %q", control.State, recovery.StateNormal)
	}

	var requestState string
	if err := finalDB.SQL().QueryRowContext(ctx, `SELECT state FROM restore_requests WHERE id = ?`, request.ID).Scan(&requestState); err != nil {
		t.Fatalf("read restore_requests.state: %v", err)
	}
	if requestState != "completed" {
		t.Fatalf("restore_requests.state = %q, want %q", requestState, "completed")
	}
}

// TestDoRestoreRejectsCorruptBackup ensures a corrupt/incomplete backup file
// never reaches the atomic replace step.
func TestDoRestoreRejectsCorruptBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnora.db")
	backupPath := filepath.Join(dir, "backup.db")

	liveDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: dbPath})
	if err != nil {
		t.Fatalf("open live database: %v", err)
	}
	if err := liveDB.Close(); err != nil {
		t.Fatalf("close live database: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte("not a sqlite file"), 0o600); err != nil {
		t.Fatalf("write corrupt backup: %v", err)
	}

	stepErr := doRestore(ctx, restoreParams{
		dbPath:      dbPath,
		backupPath:  backupPath,
		requestID:   "restore-does-not-matter",
		journalPath: recovery.DefaultJournalPath(dbPath),
		actor:       "",
	})
	if stepErr == nil {
		t.Fatal("doRestore succeeded with a corrupt backup, want a validation error")
	}

	originalContents, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read original live database after rejected restore: %v", err)
	}
	if len(originalContents) == 0 {
		t.Fatal("original live database was truncated despite the rejected restore")
	}
}
