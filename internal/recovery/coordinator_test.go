package recovery

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/store"
)

func openRecoveryTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "omnora.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCoordinatorBeginRestoreAndStateTransitionsAreAudited(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := context.Background()
	coordinator := NewCoordinator(db.SQL(), WithClock(func() time.Time {
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
	if request.ID == "" || request.State != StatePreparing || request.BackupID != "backup-1" {
		t.Fatalf("request = %#v", request)
	}
	control, err := coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if control.State != StatePreparing || control.Ready {
		t.Fatalf("control = %#v", control)
	}
	var count int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'recovery_state_transition'`).Scan(&count); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 1 {
		t.Fatalf("recovery transition audit count = %d, want 1", count)
	}

	if _, err := coordinator.BeginRestore(ctx, BeginRestoreRequest{BackupID: "backup-1", ActorAccountID: "admin"}); !errors.Is(err, ErrRestoreInProgress) {
		t.Fatalf("duplicate begin error = %v, want ErrRestoreInProgress", err)
	}
	if _, err := coordinator.MarkRestoring(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("mark restoring: %v", err)
	}
	schemaVersion := int64(11)
	request, err = coordinator.RecordArtifacts(ctx, request.ID, "admin", ArtifactUpdate{
		StagingPath:         "/secure/staging/restore-1",
		SourceSchemaVersion: &schemaVersion,
	})
	if err != nil {
		t.Fatalf("record artifacts: %v", err)
	}
	if request.StagingPath != "/secure/staging/restore-1" || !request.SourceSchemaVersion.Valid || request.SourceSchemaVersion.Int64 != 11 {
		t.Fatalf("request artifacts = %#v", request)
	}
	if _, err := coordinator.MarkRecoveryRequired(ctx, request.ID, "admin", "snapshot_validation_failed"); err != nil {
		t.Fatalf("mark recovery required: %v", err)
	}
	control, err = coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read failed control: %v", err)
	}
	if control.State != StateRecoveryRequired || control.Ready {
		t.Fatalf("failed control = %#v", control)
	}
}

func TestCoordinatorApplyRestoreRevokesCredentialClassesAtomically(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := context.Background()
	coordinator := NewCoordinator(db.SQL(), WithClock(func() time.Time {
		return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	}))

	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status, password_hash,
    totp_required, totp_secret_ciphertext, totp_pending_secret_ciphertext,
    totp_pending_expires_at, totp_confirmed_at)
VALUES ('admin', 'admin@example.com', 'Admin', 'admin', 'active', 'hash', 1,
    'active-secret', 'pending-secret', '2099-01-01T00:00:00Z', '2026-08-01T00:00:00Z')
`); err != nil {
		t.Fatalf("insert accounts: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status, password_hash)
VALUES ('member', 'member@example.com', 'Member', 'member', 'active', 'hash')
`); err != nil {
		t.Fatalf("insert member account: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO spaces(id, kind, name, owner_account_id) VALUES ('space-1', 'shared', 'Space', 'admin')`); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO space_members(space_id, account_id, permission) VALUES ('space-1', 'admin', 'manager')`); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status) VALUES ('mount-1', 'space-1', 'Docs', '/tmp/docs', 'external', 'read_write', 'active')`); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO identity_sessions(id, account_id, token_hash, created_at, expires_at, credential_generation) VALUES ('ses-1', 'admin', 'hash-1', '2026-08-01T00:00:00Z', '2099-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("insert identity session: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO browser_sessions(id, account_id, token_hash, audience, expires_at, credential_generation) VALUES ('browser-1', 'admin', 'hash-2', 'admin_web', '2099-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("insert browser session: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, expires_at, credential_generation) VALUES ('ait-1', 'ait-public', 'hash-3', 'admin', 'test', '[]', '2099-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("insert ai token: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO shares(id, public_id, secret_hash, creator_account_id, space_id, mount_id, relative_path, expires_at, credential_generation) VALUES ('share-1', 'share-public', 'hash-4', 'admin', 'space-1', 'mount-1', 'file.txt', '2099-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("insert share: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO share_sessions(id, share_id, session_hash, generation, expires_at, credential_generation) VALUES ('share-session-1', 'share-1', 'share-session-hash', 1, '2099-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("insert share session: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO upload_sessions(id, account_id, space_id, mount_id, target_relative_path, declared_size, part_size, temp_dir, expires_at, credential_generation) VALUES ('upload-1', 'admin', 'space-1', 'mount-1', 'upload.bin', 1, 1, '/tmp/upload', '2099-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("insert upload session: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO jobs(id, kind, status) VALUES ('job-1', 'index', 'running')`); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO backups(id, status, path, created_by) VALUES ('backup-1', 'completed', '/tmp/backup.db', 'admin')`); err != nil {
		t.Fatalf("insert backup: %v", err)
	}

	request, err := coordinator.BeginRestore(ctx, BeginRestoreRequest{BackupID: "backup-1", ActorAccountID: "admin"})
	if err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	if _, err := coordinator.MarkRestoring(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("mark restoring: %v", err)
	}
	if _, err := coordinator.ApplyRestoreEffects(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("apply restore effects: %v", err)
	}

	var generation string
	if err := db.SQL().QueryRowContext(ctx, `SELECT value FROM system_state WHERE key = 'credential_generation'`).Scan(&generation); err != nil {
		t.Fatalf("read credential generation: %v", err)
	}
	if generation != "2" {
		t.Fatalf("credential generation = %q, want 2", generation)
	}
	for query, name := range map[string]string{
		`SELECT COUNT(*) FROM identity_sessions WHERE revoked_at IS NOT NULL`:                    "identity session",
		`SELECT COUNT(*) FROM browser_sessions WHERE revoked_at IS NOT NULL`:                     "browser session",
		`SELECT COUNT(*) FROM ai_tokens WHERE revoked_at IS NOT NULL`:                            "AI token",
		`SELECT COUNT(*) FROM shares WHERE revoked_at IS NOT NULL AND generation = 2`:            "share",
		`SELECT COUNT(*) FROM share_sessions WHERE revoked_at IS NOT NULL`:                       "share session",
		`SELECT COUNT(*) FROM upload_sessions WHERE status = 'canceled' AND cleanup_pending = 1`: "upload",
		`SELECT COUNT(*) FROM jobs WHERE status = 'paused'`:                                      "job",
		`SELECT COUNT(*) FROM mounts WHERE status = 'disabled'`:                                  "mount",
	} {
		var count int
		if err := db.SQL().QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("%s count = %d, want 1", name, count)
		}
	}
	var adminTOTPRequired, adminTOTPReset, memberTOTPRequired, passwordReset int
	if err := db.SQL().QueryRowContext(ctx, `SELECT totp_required, totp_reset_required, password_reset_required FROM accounts WHERE id = 'admin'`).Scan(&adminTOTPRequired, &adminTOTPReset, &passwordReset); err != nil {
		t.Fatalf("read admin reset flags: %v", err)
	}
	if adminTOTPRequired != 0 || adminTOTPReset != 1 || passwordReset != 1 {
		t.Fatalf("admin reset flags = %d/%d/%d", adminTOTPRequired, adminTOTPReset, passwordReset)
	}
	if err := db.SQL().QueryRowContext(ctx, `SELECT totp_required FROM accounts WHERE id = 'member'`).Scan(&memberTOTPRequired); err != nil {
		t.Fatalf("read member totp flag: %v", err)
	}
	if memberTOTPRequired != 0 {
		t.Fatalf("member totp_required = %d, want 0", memberTOTPRequired)
	}
	control, err := coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if control.State != StateFinalizeRequired || control.Ready {
		t.Fatalf("control after effects = %#v", control)
	}
}

func TestCoordinatorCompleteBootstrapWritesCompletedRestoreRequestState(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := context.Background()
	coordinator := NewCoordinator(db.SQL(), WithClock(func() time.Time {
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
	if _, err := coordinator.MarkRestoring(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("mark restoring: %v", err)
	}
	if _, err := coordinator.ApplyRestoreEffects(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("apply restore effects: %v", err)
	}
	if _, err := coordinator.MarkNormalPendingBootstrap(ctx, request.ID, "admin"); err != nil {
		t.Fatalf("mark normal pending bootstrap: %v", err)
	}

	completed, err := coordinator.CompleteBootstrap(ctx, request.ID, "admin")
	if err != nil {
		t.Fatalf("complete bootstrap: %v", err)
	}
	if completed.State != StateNormal {
		t.Fatalf("completed request state = %q, want %q", completed.State, StateNormal)
	}

	// restore_requests.state has a narrower CHECK constraint than
	// recovery_control.state and never accepted the literal 'normal' value;
	// CompleteBootstrap must map the terminal outcome to 'completed' there
	// while recovery_control keeps 'normal'.
	var requestState string
	if err := db.SQL().QueryRowContext(ctx, `SELECT state FROM restore_requests WHERE id = ?`, request.ID).Scan(&requestState); err != nil {
		t.Fatalf("read restore_requests state: %v", err)
	}
	if requestState != "completed" {
		t.Fatalf("restore_requests.state = %q, want %q", requestState, "completed")
	}

	control, err := coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if control.State != StateNormal {
		t.Fatalf("control.State = %q, want %q", control.State, StateNormal)
	}
	if control.Ready {
		t.Fatalf("control.Ready = true, want false: MarkReady owns the aggregate readiness gate")
	}
}

func TestCoordinatorAuditFailureRollsBackTransition(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := context.Background()
	coordinator := NewCoordinator(db.SQL(), WithAuditWriter(func(context.Context, *sql.Tx, AuditEvent) error {
		return errors.New("audit unavailable")
	}))
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status, password_hash)
VALUES ('admin', 'admin@example.com', 'Admin', 'admin', 'active', 'hash')
`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO backups(id, status, path, created_by) VALUES ('backup-1', 'completed', '/tmp/backup.db', 'admin')`); err != nil {
		t.Fatalf("insert backup: %v", err)
	}
	if _, err := coordinator.BeginRestore(ctx, BeginRestoreRequest{BackupID: "backup-1", ActorAccountID: "admin"}); err == nil {
		t.Fatal("begin restore succeeded despite audit failure")
	}
	control, err := coordinator.Control(ctx)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if control.State != StateNormal || control.Ready {
		t.Fatalf("control after rollback = %#v", control)
	}
	var count int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM restore_requests`).Scan(&count); err != nil {
		t.Fatalf("count restore requests: %v", err)
	}
	if count != 0 {
		t.Fatalf("restore requests = %d, want 0 after rollback", count)
	}
}
