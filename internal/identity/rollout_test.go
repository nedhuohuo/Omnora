package identity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrepareSessionPurposeRolloutBackfillsLegacySessionsWithoutRevokingAdmin(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)

	member, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("CreateAccount(member) error = %v", err)
	}
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "admin@example.com",
		DisplayName: "Admin",
		Password:    "CorrectHorse1!",
		Role:        "admin",
	})
	if err != nil {
		t.Fatalf("CreateAccount(admin) error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE accounts SET totp_required = 0, totp_confirmed_at = NULL WHERE id = ?
`, admin.Account.ID); err != nil {
		t.Fatalf("clear admin TOTP state: %v", err)
	}

	const expiresAt = "2030-01-01T00:00:00Z"
	for id, accountID := range map[string]string{
		"legacy-member-session": member.Account.ID,
		"legacy-admin-session":  admin.Account.ID,
	} {
		if _, err := db.ExecContext(ctx, `
INSERT INTO identity_sessions(id, account_id, token_hash, entry, credential_generation, created_at, expires_at)
VALUES (?, ?, ?, 'http', 7, '2026-01-01T00:00:00Z', ?)
`, id, accountID, "sha256:"+id, expiresAt); err != nil {
			t.Fatalf("insert legacy session %s: %v", id, err)
		}
	}

	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := PrepareSessionPurposeRollout(ctx, db, now); err != nil {
		t.Fatalf("PrepareSessionPurposeRollout() error = %v", err)
	}

	assertPurpose := func(id string, want SessionPurpose) {
		t.Helper()
		var purpose string
		if err := db.QueryRowContext(ctx, "SELECT COALESCE(purpose, '') FROM identity_sessions WHERE id = ?", id).Scan(&purpose); err != nil {
			t.Fatalf("query purpose %s: %v", id, err)
		}
		if SessionPurpose(purpose) != want {
			t.Fatalf("purpose %s = %q, want %q", id, purpose, want)
		}
	}
	assertPurpose("legacy-member-session", SessionPurposeFull)
	assertPurpose("legacy-admin-session", SessionPurposeFull)
	var revokedAt string
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(revoked_at, '') FROM identity_sessions WHERE id = 'legacy-admin-session'").Scan(&revokedAt); err != nil {
		t.Fatalf("query admin session: %v", err)
	}
	if revokedAt != "" {
		t.Fatalf("legacy admin session was revoked = %q, want empty", revokedAt)
	}
}

func TestPrepareSessionPurposeRolloutLeavesExpiredAdminSessionUnchanged(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email: "expired-admin@example.com", DisplayName: "Expired Admin", Password: "CorrectHorse1!", Role: "admin",
	})
	if err != nil {
		t.Fatalf("CreateAccount(admin) error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE accounts SET totp_required = 0 WHERE id = ?`, admin.Account.ID); err != nil {
		t.Fatalf("clear admin TOTP state: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO identity_sessions(id, account_id, token_hash, entry, credential_generation, created_at, expires_at)
VALUES ('expired-admin-session', ?, 'sha256:expired', 'http', 7, '2026-01-01T00:00:00Z', '2026-08-06T11:00:00Z')
`, admin.Account.ID); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := PrepareSessionPurposeRollout(ctx, db, now); err != nil {
		t.Fatalf("PrepareSessionPurposeRollout() error = %v", err)
	}
	var purpose, revokedAt string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(purpose, ''), COALESCE(revoked_at, '') FROM identity_sessions WHERE id = 'expired-admin-session'`).Scan(&purpose, &revokedAt); err != nil {
		t.Fatalf("query expired session: %v", err)
	}
	if purpose != "" || revokedAt != "" {
		t.Fatalf("expired session changed: purpose=%q revoked_at=%q", purpose, revokedAt)
	}
}

func TestPrepareSessionPurposeRolloutRejectsUnknownActivePurpose(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	created, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "member@example.com",
		DisplayName: "Member",
		Password:    "CorrectHorse1!",
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO identity_sessions(id, account_id, token_hash, entry, purpose, credential_generation, created_at, expires_at)
VALUES ('unknown-purpose-session', ?, 'sha256:unknown', 'http', 'legacy', 7, '2026-01-01T00:00:00Z', ?)
`, created.Account.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert unknown-purpose session: %v", err)
	}

	if err := PrepareSessionPurposeRollout(ctx, db, time.Now().UTC()); !errors.Is(err, ErrSessionPurposeInvariant) {
		t.Fatalf("PrepareSessionPurposeRollout() error = %v, want ErrSessionPurposeInvariant", err)
	}
}
