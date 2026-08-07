package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"omnora/internal/domain"
)

func TestTOTPSetupKeepsConfirmedSecretActiveUntilPromotion(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "totp-setup@example.com",
		DisplayName: "TOTP Setup",
		Password:    "CorrectHorse1!",
		Role:        domain.AccountRoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	active := "v1:active"
	if _, err := db.ExecContext(ctx, `
UPDATE accounts
SET totp_required = 1, totp_secret_ciphertext = ?, totp_confirmed_at = ?
WHERE id = ?
`, active, "2026-08-06T12:00:00Z", admin.Account.ID); err != nil {
		t.Fatalf("seed active TOTP: %v", err)
	}

	expiresAt := time.Date(2026, 8, 6, 12, 10, 0, 0, time.UTC)
	if err := svc.SavePendingTOTP(ctx, admin.Account.ID, "v1:pending", expiresAt); err != nil {
		t.Fatalf("SavePendingTOTP() error = %v", err)
	}

	state, err := svc.LoadTOTPState(ctx, admin.Account.ID)
	if err != nil {
		t.Fatalf("LoadTOTPState() error = %v", err)
	}
	if !state.Required || state.ActiveCiphertext != active || state.PendingCiphertext != "v1:pending" || !state.PendingExpiresAt.Equal(expiresAt) {
		t.Fatalf("TOTP state = %#v, want active and pending values preserved", state)
	}

	var gotActive, gotPending string
	if err := db.QueryRowContext(ctx, `SELECT totp_secret_ciphertext, totp_pending_secret_ciphertext FROM accounts WHERE id = ?`, admin.Account.ID).Scan(&gotActive, &gotPending); err != nil {
		t.Fatalf("query TOTP state: %v", err)
	}
	if gotActive != active || gotPending != "v1:pending" {
		t.Fatalf("stored TOTP state = active %q pending %q", gotActive, gotPending)
	}
}

func TestTOTPConfirmPromotesPendingSecretAndRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "totp-confirm@example.com",
		DisplayName: "TOTP Confirm",
		Password:    "CorrectHorse1!",
		Role:        domain.AccountRoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE accounts SET totp_required = 0, totp_secret_ciphertext = 'v1:old', totp_confirmed_at = NULL,
totp_pending_secret_ciphertext = 'v1:new', totp_pending_expires_at = '2026-08-02T12:10:00Z'
WHERE id = ?
`, admin.Account.ID); err != nil {
		t.Fatalf("seed pending TOTP: %v", err)
	}
	current, err := svc.CreateSession(ctx, SessionRequest{AccountID: admin.Account.ID, TTL: time.Hour, Purpose: SessionPurposeTOTPEnrollment})
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	other, err := svc.CreateSession(ctx, SessionRequest{AccountID: admin.Account.ID, TTL: time.Hour, Purpose: SessionPurposeFull})
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	rotated, err := svc.PromotePendingTOTPAndRotateSession(ctx, current.Session, "v1:new", now)
	if err != nil {
		t.Fatalf("PromotePendingTOTPAndRotateSession() error = %v", err)
	}
	if rotated.Token == "" || rotated.Session.Purpose != SessionPurposeFull {
		t.Fatalf("rotated session = %#v", rotated)
	}

	var active, pending string
	var required int
	var confirmed string
	if err := db.QueryRowContext(ctx, `SELECT totp_secret_ciphertext, COALESCE(totp_pending_secret_ciphertext, ''), totp_required, COALESCE(totp_confirmed_at, '') FROM accounts WHERE id = ?`, admin.Account.ID).Scan(&active, &pending, &required, &confirmed); err != nil {
		t.Fatalf("query promoted TOTP: %v", err)
	}
	if active != "v1:new" || pending != "" || required != 1 || confirmed == "" {
		t.Fatalf("promoted TOTP = active %q pending %q required %d confirmed %q", active, pending, required, confirmed)
	}
	var currentRevoked string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(revoked_at, '') FROM identity_sessions WHERE id = ?`, current.Session.ID).Scan(&currentRevoked); err != nil {
		t.Fatalf("query current session: %v", err)
	}
	if currentRevoked == "" {
		t.Fatal("current enrollment session was not rotated away")
	}
	var otherRevoked string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(revoked_at, '') FROM identity_sessions WHERE id = ?`, other.Session.ID).Scan(&otherRevoked); err != nil {
		t.Fatalf("query other session: %v", err)
	}
	if otherRevoked == "" {
		t.Fatal("other session was not revoked")
	}
	if _, err := svc.VerifySession(ctx, rotated.Token); err != nil {
		t.Fatalf("verify rotated session: %v", err)
	}
}

func TestTOTPConfirmRejectsExpiredPendingSecret(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "totp-expired@example.com",
		DisplayName: "TOTP Expired",
		Password:    "CorrectHorse1!",
		Role:        domain.AccountRoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	expired := time.Date(2026, 8, 2, 11, 59, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `
UPDATE accounts SET totp_pending_secret_ciphertext = ?, totp_pending_expires_at = ? WHERE id = ?
`, "v1:expired", formatTime(expired), admin.Account.ID); err != nil {
		t.Fatalf("seed expired pending TOTP: %v", err)
	}
	if err := svc.PromotePendingTOTP(ctx, admin.Account.ID, "v1:expired", time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("PromotePendingTOTP() error = %v, want ErrInvalidCredential", err)
	}
	var active, pending string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(totp_secret_ciphertext, ''), COALESCE(totp_pending_secret_ciphertext, '') FROM accounts WHERE id = ?`, admin.Account.ID).Scan(&active, &pending); err != nil {
		t.Fatalf("query TOTP after expiry: %v", err)
	}
	if active != "" || pending != "v1:expired" {
		t.Fatalf("expired promotion changed TOTP state: active=%q pending=%q", active, pending)
	}
}

func TestAdminCannotDisableTOTP(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	admin, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "totp-disable-admin@example.com",
		DisplayName: "TOTP Admin",
		Password:    "CorrectHorse1!",
		Role:        domain.AccountRoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE accounts SET totp_required = 1, totp_secret_ciphertext = 'v1:active' WHERE id = ?`, admin.Account.ID); err != nil {
		t.Fatalf("enable TOTP: %v", err)
	}
	if err := svc.DisableTOTP(ctx, admin.Account.ID); !errors.Is(err, ErrAdminTOTPRequired) {
		t.Fatalf("DisableTOTP() error = %v, want ErrAdminTOTPRequired", err)
	}
	var required int
	if err := db.QueryRowContext(ctx, `SELECT totp_required FROM accounts WHERE id = ?`, admin.Account.ID).Scan(&required); err != nil {
		t.Fatalf("query admin TOTP: %v", err)
	}
	if required != 1 {
		t.Fatalf("admin TOTP required = %d, want 1", required)
	}
}

func TestDisableTOTPRemovesPendingMemberState(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newTestService(db)
	member, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email:       "totp-disable-member@example.com",
		DisplayName: "TOTP Member",
		Password:    "CorrectHorse1!",
		Role:        domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE accounts
SET totp_required = 1, totp_secret_ciphertext = 'v1:active', totp_confirmed_at = ?,
    totp_pending_secret_ciphertext = 'v1:pending', totp_pending_expires_at = ?
WHERE id = ?
`, formatTime(time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)), formatTime(time.Date(2026, 8, 2, 12, 10, 0, 0, time.UTC)), member.Account.ID); err != nil {
		t.Fatalf("seed member TOTP state: %v", err)
	}
	if err := svc.DisableTOTP(ctx, member.Account.ID); err != nil {
		t.Fatalf("DisableTOTP() error = %v", err)
	}
	var active, pending string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(totp_secret_ciphertext, ''), COALESCE(totp_pending_secret_ciphertext, '') FROM accounts WHERE id = ?`, member.Account.ID).Scan(&active, &pending); err != nil {
		t.Fatalf("query disabled TOTP state: %v", err)
	}
	if active != "" || pending != "" {
		t.Fatalf("disabled TOTP state = active %q pending %q, want both empty", active, pending)
	}
}
