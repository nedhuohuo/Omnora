// Command omnora-recovery performs the offline half of an Omnora backup
// restore: staging, integrity/schema validation, forward migration, a
// pre-restore safe snapshot, the atomic database replacement, rehydrating
// the restore request from the durable recovery journal if the replacement
// wiped it out of the database, and revoking every credential class via the
// recovery coordinator. It must never be reachable over HTTP: the main
// omnora process schedules a controlled shutdown as soon as a restore is
// accepted specifically so this tool is the only thing touching the SQLite
// file while a restore is in flight.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"omnora/internal/recovery"
	"omnora/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "restore":
		runRestore(os.Args[2:])
	case "finalize":
		runFinalize(os.Args[2:])
	case "status":
		runStatus(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "omnora-recovery: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`omnora-recovery performs the offline half of an Omnora backup restore.

It must run while the main omnora process is stopped. Every "restore
accepted" HTTP response triggers a controlled shutdown of that process for
exactly this reason: this tool must be the only thing touching the SQLite
file during a restore.

Usage:
  omnora-recovery restore  --db <path> --backup <path> --request <id> [--journal <path>] [--actor <id>]
  omnora-recovery finalize --db <path> --request <id> [--journal <path>] [--actor <id>]
  omnora-recovery status   --db <path> [--journal <path>]

restore   validates the backup snapshot, stages and forward-migrates a copy
          of it, snapshots the current live database for rollback, atomically
          replaces the live database with the migrated staged copy,
          rehydrates the restore request from the recovery journal if the
          replacement wiped it out of the freshly replaced database, and
          revokes every credential class via ApplyRestoreEffects. It leaves
          recovery_control in normal_pending_bootstrap.

finalize  completes the restore by moving recovery_control from
          normal_pending_bootstrap back to normal. Run it once an operator
          has confirmed the instance is ready to resume normal service.

status    prints the current recovery_control row.
`)
}

func runRestore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the live SQLite database file that will be replaced")
	backupPath := fs.String("backup", "", "path to the completed backup snapshot to restore")
	requestID := fs.String("request", "", "restore request id (matches restore_requests.id and the recovery journal)")
	journalFlag := fs.String("journal", "", "path to the recovery journal sidecar file (default: <db>.recovery-journal.json)")
	actor := fs.String("actor", "", "admin account id recorded on recovery audit events (audit_events.actor_account_id references accounts(id); leave empty for an unattributed system actor)")
	_ = fs.Parse(args)

	*dbPath = strings.TrimSpace(*dbPath)
	*backupPath = strings.TrimSpace(*backupPath)
	*requestID = strings.TrimSpace(*requestID)
	if *dbPath == "" || *backupPath == "" || *requestID == "" {
		fmt.Fprintln(os.Stderr, "omnora-recovery restore: --db, --backup, and --request are required")
		fs.Usage()
		os.Exit(2)
	}
	journalPath := resolveJournalPath(*journalFlag, *dbPath)
	if err := doRestore(context.Background(), restoreParams{
		dbPath:      *dbPath,
		backupPath:  *backupPath,
		requestID:   *requestID,
		journalPath: journalPath,
		actor:       *actor,
		log:         func(format string, a ...any) { fmt.Printf(format+"\n", a...) },
	}); err != nil {
		fatal(err.step, err.cause)
	}
	fmt.Println("restore complete: recovery_control is now normal_pending_bootstrap")
	fmt.Println("start omnora normally to complete bootstrap, then run `omnora-recovery finalize` to return to normal")
}

type restoreParams struct {
	dbPath      string
	backupPath  string
	requestID   string
	journalPath string
	actor       string
	log         func(format string, a ...any)
}

type stepError struct {
	step  string
	cause error
}

func (e *stepError) Error() string { return e.step + ": " + e.cause.Error() }

func wrapStep(step string, cause error) *stepError {
	if cause == nil {
		return nil
	}
	return &stepError{step: step, cause: cause}
}

// doRestore performs the full offline restore: validate the backup, stage
// and forward-migrate a copy of it, snapshot the current live database,
// atomically replace it, rehydrate the restore request from the journal if
// the replacement wiped it out, and drive the coordinator through
// ApplyRestoreEffects to normal_pending_bootstrap. It returns rather than
// exiting so it is exercised directly by tests.
func doRestore(ctx context.Context, p restoreParams) *stepError {
	if p.log == nil {
		p.log = func(string, ...any) {}
	}

	p.log("validating backup snapshot %s", p.backupPath)
	if err := recovery.ValidateSnapshot(ctx, p.backupPath); err != nil {
		return wrapStep("backup snapshot failed validation", err)
	}

	stagingPath := p.dbPath + ".restore-staging-" + p.requestID + ".db"
	defer os.Remove(stagingPath)
	p.log("staging the backup snapshot for forward migration")
	if err := copyFileAtomic(p.backupPath, stagingPath, 0o600); err != nil {
		return wrapStep("stage backup snapshot", err)
	}

	stagingDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: stagingPath})
	if err != nil {
		// store.OpenSQLite rejects a schema newer than this binary supports,
		// so this also covers "new schema rejected" without special-casing it.
		return wrapStep("open staged snapshot (schema is too new or the file is corrupt)", err)
	}
	if err := stagingDB.IntegrityCheck(ctx); err != nil {
		_ = stagingDB.Close()
		return wrapStep("staged snapshot failed integrity check after migration", err)
	}
	if err := stagingDB.Close(); err != nil {
		return wrapStep("close staged snapshot", err)
	}

	safeSnapshotPath := p.dbPath + ".pre-restore-safe-snapshot-" + p.requestID + ".db"
	p.log("creating a pre-restore safe snapshot of the current live database")
	if err := createSafeSnapshot(ctx, p.dbPath, safeSnapshotPath); err != nil {
		return wrapStep("create pre-restore safe snapshot", err)
	}

	p.log("atomically replacing the live database with the staged, migrated snapshot")
	if err := os.Rename(stagingPath, p.dbPath); err != nil {
		return wrapStep("replace live database (original database and safe snapshot are untouched)", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(p.dbPath + suffix)
	}
	if err := syncDir(filepath.Dir(p.dbPath)); err != nil {
		return wrapStep("sync database directory after replace", err)
	}

	liveDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: p.dbPath})
	if err != nil {
		return wrapStep("open replaced live database; restore safe snapshot from "+safeSnapshotPath, err)
	}
	defer liveDB.Close()

	coordinator := recovery.NewCoordinator(liveDB.SQL(), recovery.WithJournalPath(p.journalPath))
	rehydrated, err := coordinator.RehydrateFromJournal(ctx)
	if err != nil {
		return wrapStep("rehydrate restore request from recovery journal", err)
	}
	if rehydrated {
		p.log("the database replacement wiped the restore request; rehydrated it from the recovery journal")
	}

	if _, err := coordinator.MarkRestoring(ctx, p.requestID, p.actor); err != nil {
		return wrapStep("mark restoring", err)
	}
	if _, err := coordinator.RecordArtifacts(ctx, p.requestID, p.actor, recovery.ArtifactUpdate{
		StagingPath:      stagingPath,
		SafeSnapshotPath: safeSnapshotPath,
	}); err != nil {
		return wrapStep("record staging/safe-snapshot artifacts", err)
	}
	if _, err := coordinator.ApplyRestoreEffects(ctx, p.requestID, p.actor); err != nil {
		return wrapStep("apply restore effects (credential revocation)", err)
	}
	if _, err := coordinator.MarkNormalPendingBootstrap(ctx, p.requestID, p.actor); err != nil {
		return wrapStep("mark normal pending bootstrap", err)
	}
	return nil
}

func runFinalize(args []string) {
	fs := flag.NewFlagSet("finalize", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the live SQLite database file")
	requestID := fs.String("request", "", "restore request id to finalize")
	journalFlag := fs.String("journal", "", "path to the recovery journal sidecar file (default: <db>.recovery-journal.json)")
	actor := fs.String("actor", "", "admin account id recorded on recovery audit events (audit_events.actor_account_id references accounts(id); leave empty for an unattributed system actor)")
	_ = fs.Parse(args)

	*dbPath = strings.TrimSpace(*dbPath)
	*requestID = strings.TrimSpace(*requestID)
	if *dbPath == "" || *requestID == "" {
		fmt.Fprintln(os.Stderr, "omnora-recovery finalize: --db and --request are required")
		fs.Usage()
		os.Exit(2)
	}
	journalPath := resolveJournalPath(*journalFlag, *dbPath)
	requestState, err := doFinalize(context.Background(), *dbPath, *requestID, journalPath, *actor)
	if err != nil {
		fatal(err.step, err.cause)
	}
	fmt.Printf("finalize complete: restore request %s is now %s; recovery_control is normal\n", *requestID, requestState)
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: could not remove recovery journal %s: %v\n", journalPath, err)
	}
}

// doFinalize moves recovery_control from normal_pending_bootstrap back to
// normal. It returns rather than exiting so it is exercised directly by
// tests.
func doFinalize(ctx context.Context, dbPath, requestID, journalPath, actor string) (recovery.State, *stepError) {
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: dbPath})
	if err != nil {
		return "", wrapStep("open live database", err)
	}
	defer db.Close()

	coordinator := recovery.NewCoordinator(db.SQL(), recovery.WithJournalPath(journalPath))
	request, err := coordinator.CompleteBootstrap(ctx, requestID, actor)
	if err != nil {
		return "", wrapStep("complete bootstrap", err)
	}
	return request.State, nil
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the live SQLite database file")
	journalFlag := fs.String("journal", "", "path to the recovery journal sidecar file (default: <db>.recovery-journal.json)")
	_ = fs.Parse(args)

	*dbPath = strings.TrimSpace(*dbPath)
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "omnora-recovery status: --db is required")
		fs.Usage()
		os.Exit(2)
	}
	journalPath := resolveJournalPath(*journalFlag, *dbPath)
	ctx := context.Background()

	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: *dbPath})
	if err != nil {
		fatal("open live database", err)
	}
	defer db.Close()

	control, err := recovery.NewCoordinator(db.SQL(), recovery.WithJournalPath(journalPath)).Control(ctx)
	if err != nil {
		fatal("read recovery control", err)
	}
	fmt.Printf("state=%s ready=%v requestId=%s reasonCode=%s cleanupPending=%v\n",
		control.State, control.Ready, control.RequestID, control.ReasonCode, control.CleanupPending)
}

func resolveJournalPath(flagValue, dbPath string) string {
	if strings.TrimSpace(flagValue) != "" {
		return strings.TrimSpace(flagValue)
	}
	return recovery.DefaultJournalPath(dbPath)
}

// createSafeSnapshot backs up the still-untouched live database at dbPath so
// a failed or misdiagnosed restore can be rolled back by hand. It uses the
// SQLite Online Backup API rather than a plain file copy so it is correct
// regardless of pending WAL contents.
func createSafeSnapshot(ctx context.Context, dbPath, safeSnapshotPath string) error {
	liveDB, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: dbPath})
	if err != nil {
		return err
	}
	defer liveDB.Close()
	if err := liveDB.BackupTo(ctx, safeSnapshotPath); err != nil {
		return err
	}
	return os.Chmod(safeSnapshotPath, 0o600)
}

// copyFileAtomic copies src to dst via a same-directory temp file, fsync,
// and rename so a crash mid-copy never leaves a partially written dst.
func copyFileAtomic(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tempPath := dst + ".tmp"
	out, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, dst); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "omnora-recovery: %s: %v\n", step, err)
	os.Exit(1)
}
