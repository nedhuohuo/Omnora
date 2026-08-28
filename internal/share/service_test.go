package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"omnora/internal/store"
)

func TestExchangeCreatesSessionAndConsumesOneVisit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	insertShare(t, db, testShare{
		ID:             "share-1",
		PublicID:       "public-1",
		FragmentSecret: "fragment-secret",
		MaxVisits:      sql.NullInt64{Int64: 2, Valid: true},
		UsedVisits:     1,
		ExpiresAt:      now.Add(time.Hour),
	})

	service := NewService(db, WithClock(func() time.Time { return now }), WithSessionTTL(10*time.Minute))
	result, err := service.Exchange(ctx, ExchangeRequest{
		PublicID:       "public-1",
		FragmentSecret: "fragment-secret",
	})
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if result.SessionToken == "" {
		t.Fatal("Exchange() returned empty session token")
	}
	if result.Code != "" || result.PasswordRequired {
		t.Fatalf("Exchange() result code = %q passwordRequired = %v, want success", result.Code, result.PasswordRequired)
	}
	if !result.ExpiresAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("ExpiresAt = %s, want %s", result.ExpiresAt, now.Add(10*time.Minute))
	}

	var usedVisits int
	if err := db.QueryRowContext(ctx, "SELECT used_visits FROM shares WHERE id = ?", "share-1").Scan(&usedVisits); err != nil {
		t.Fatalf("query used_visits: %v", err)
	}
	if usedVisits != 2 {
		t.Fatalf("used_visits = %d, want 2", usedVisits)
	}

	var storedSessionHash string
	if err := db.QueryRowContext(ctx, "SELECT session_hash FROM share_sessions WHERE share_id = ?", "share-1").Scan(&storedSessionHash); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if storedSessionHash == result.SessionToken {
		t.Fatal("session token was stored in plaintext")
	}
	if !VerifySecret(result.SessionToken, storedSessionHash) {
		t.Fatal("stored session hash does not verify returned token")
	}
}

func TestShareSessionsFailAfterMountSharingOrGrantIsRevoked(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	insertShare(t, db, testShare{
		ID: "share-access", PublicID: "public-access", FragmentSecret: "fragment-secret", ExpiresAt: now.Add(time.Hour),
	})
	service := NewService(db, WithClock(func() time.Time { return now }))
	issued, err := service.Exchange(ctx, ExchangeRequest{PublicID: "public-access", FragmentSecret: "fragment-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE mounts SET allow_public_shares = 0 WHERE id = 'mount-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifySession(ctx, issued.SessionToken); err == nil {
		t.Fatal("expected session to fail after public sharing was disabled")
	}
	if _, err := db.ExecContext(ctx, `UPDATE mounts SET allow_public_shares = 1 WHERE id = 'mount-1'; DELETE FROM mount_account_grants WHERE mount_id = 'mount-1' AND account_id = 'account-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Exchange(ctx, ExchangeRequest{PublicID: "public-access", FragmentSecret: "fragment-secret"}); err == nil {
		t.Fatal("expected exchange to fail after creator grant removal")
	}
}

func TestExchangePasswordRequiredDoesNotConsumeVisit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	passwordHash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	insertShare(t, db, testShare{
		ID:             "share-2",
		PublicID:       "public-2",
		FragmentSecret: "fragment-secret",
		PasswordHash:   sql.NullString{String: passwordHash, Valid: true},
		MaxVisits:      sql.NullInt64{Int64: 1, Valid: true},
		ExpiresAt:      now.Add(time.Hour),
	})

	service := NewService(db, WithClock(func() time.Time { return now }))
	result, err := service.Exchange(ctx, ExchangeRequest{
		PublicID:       "public-2",
		FragmentSecret: "fragment-secret",
	})
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if result.Code != CodePasswordRequired || !result.PasswordRequired {
		t.Fatalf("Exchange() result = %+v, want password_required", result)
	}
	assertVisitCount(t, db, "share-2", 0)
	assertSessionCount(t, db, "share-2", 0)
}

func TestExchangePasswordSuccessConsumesVisit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	passwordHash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	insertShare(t, db, testShare{
		ID:             "share-3",
		PublicID:       "public-3",
		FragmentSecret: "fragment-secret",
		PasswordHash:   sql.NullString{String: passwordHash, Valid: true},
		MaxVisits:      sql.NullInt64{Int64: 1, Valid: true},
		ExpiresAt:      now.Add(time.Hour),
	})

	service := NewService(db, WithClock(func() time.Time { return now }))
	_, err = service.Exchange(ctx, ExchangeRequest{
		PublicID:       "public-3",
		FragmentSecret: "fragment-secret",
		Password:       "correct horse",
	})
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	assertVisitCount(t, db, "share-3", 1)
	assertSessionCount(t, db, "share-3", 1)
}

func TestExchangeFailuresDoNotConsumeVisitsOrEnumerate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	passwordHash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	insertShare(t, db, testShare{
		ID:             "share-4",
		PublicID:       "public-4",
		FragmentSecret: "fragment-secret",
		PasswordHash:   sql.NullString{String: passwordHash, Valid: true},
		MaxVisits:      sql.NullInt64{Int64: 1, Valid: true},
		ExpiresAt:      now.Add(time.Hour),
	})

	service := NewService(db, WithClock(func() time.Time { return now }))
	for _, tc := range []struct {
		name string
		req  ExchangeRequest
	}{
		{
			name: "wrong public id",
			req: ExchangeRequest{
				PublicID:       "missing",
				FragmentSecret: "fragment-secret",
				Password:       "correct horse",
			},
		},
		{
			name: "wrong fragment secret",
			req: ExchangeRequest{
				PublicID:       "public-4",
				FragmentSecret: "wrong-secret",
				Password:       "correct horse",
			},
		},
		{
			name: "wrong password",
			req: ExchangeRequest{
				PublicID:       "public-4",
				FragmentSecret: "fragment-secret",
				Password:       "wrong",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Exchange(ctx, tc.req)
			assertExchangeCode(t, err, CodeShareUnavailable)
			assertVisitCount(t, db, "share-4", 0)
			assertSessionCount(t, db, "share-4", 0)
		})
	}
}

func TestExchangeMissingSecretUsesStableCode(t *testing.T) {
	service := NewService(openTestDB(t))
	_, err := service.Exchange(context.Background(), ExchangeRequest{PublicID: "public-1"})
	assertExchangeCode(t, err, CodeShareSecretMissing)
}

func TestExchangeVisitLimitIsAtomicAndUnavailableWhenExhausted(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	insertShare(t, db, testShare{
		ID:             "share-5",
		PublicID:       "public-5",
		FragmentSecret: "fragment-secret",
		MaxVisits:      sql.NullInt64{Int64: 1, Valid: true},
		ExpiresAt:      now.Add(time.Hour),
	})

	service := NewService(db, WithClock(func() time.Time { return now }))
	_, err := service.Exchange(ctx, ExchangeRequest{
		PublicID:       "public-5",
		FragmentSecret: "fragment-secret",
	})
	if err != nil {
		t.Fatalf("first Exchange() error = %v", err)
	}

	_, err = service.Exchange(ctx, ExchangeRequest{
		PublicID:       "public-5",
		FragmentSecret: "fragment-secret",
	})
	assertExchangeCode(t, err, CodeShareUnavailable)
	assertVisitCount(t, db, "share-5", 1)
	assertSessionCount(t, db, "share-5", 1)
}

func TestHashHelpersUseConstantTimeVerifiers(t *testing.T) {
	secretHash := HashSecret("secret")
	if secretHash == "secret" {
		t.Fatal("secret hash is plaintext")
	}
	if !VerifySecret("secret", secretHash) {
		t.Fatal("VerifySecret() rejected correct secret")
	}
	if VerifySecret("wrong", secretHash) {
		t.Fatal("VerifySecret() accepted wrong secret")
	}

	passwordHash, err := HashPassword("password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if passwordHash == "password" {
		t.Fatal("password hash is plaintext")
	}
	if !VerifyPassword("password", passwordHash) {
		t.Fatal("VerifyPassword() rejected correct password")
	}
	if VerifyPassword("wrong", passwordHash) {
		t.Fatal("VerifyPassword() accepted wrong password")
	}
}

type testShare struct {
	ID             string
	PublicID       string
	FragmentSecret string
	PasswordHash   sql.NullString
	MaxVisits      sql.NullInt64
	UsedVisits     int
	ExpiresAt      time.Time
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        fmt.Sprintf("file:%s?mode=memory&cache=shared", name),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return db.SQL()
}

func insertShare(t *testing.T, db *sql.DB, share testShare) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
INSERT INTO accounts(id, email, display_name, role, status)
VALUES ('account-1', 'owner@example.test', 'Owner', 'member', 'active')
ON CONFLICT(id) DO NOTHING
`)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('space-1', 'shared', 'Space', 'account-1', 'active')
ON CONFLICT(id) DO NOTHING
`)
	if err != nil {
		t.Fatalf("insert space: %v", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO space_members(space_id, account_id, permission)
VALUES ('space-1', 'account-1', 'manager')
ON CONFLICT(space_id, account_id) DO NOTHING
`)
	if err != nil {
		t.Fatalf("insert space member: %v", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, status, allow_public_shares)
VALUES ('mount-1', 'space-1', 'Mount', '/tmp/omnora', 'managed', 'read_only', 'active', 1)
ON CONFLICT(id) DO NOTHING
`)
	if err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO mount_account_grants(mount_id, account_id, permission)
VALUES ('mount-1', 'account-1', 'manager')
ON CONFLICT(mount_id, account_id) DO NOTHING
`)
	if err != nil {
		t.Fatalf("insert mount grant: %v", err)
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO shares(
	id, public_id, secret_hash, password_hash, creator_account_id, space_id, mount_id,
	relative_path, max_visits, used_visits, expires_at
) VALUES (?, ?, ?, ?, 'account-1', 'space-1', 'mount-1', 'docs', ?, ?, ?)
`, share.ID, share.PublicID, HashSecret(share.FragmentSecret), share.PasswordHash, share.MaxVisits, share.UsedVisits, formatSQLiteTime(share.ExpiresAt))
	if err != nil {
		t.Fatalf("insert share: %v", err)
	}
}

func assertExchangeCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var exchangeErr *ExchangeError
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("Exchange() error = %v, want ExchangeError", err)
	}
	if exchangeErr.Code != code {
		t.Fatalf("Exchange() code = %q, want %q", exchangeErr.Code, code)
	}
}

func assertVisitCount(t *testing.T, db *sql.DB, shareID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT used_visits FROM shares WHERE id = ?", shareID).Scan(&got); err != nil {
		t.Fatalf("query used_visits: %v", err)
	}
	if got != want {
		t.Fatalf("used_visits = %d, want %d", got, want)
	}
}

func assertSessionCount(t *testing.T, db *sql.DB, shareID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(1) FROM share_sessions WHERE share_id = ?", shareID).Scan(&got); err != nil {
		t.Fatalf("query share_sessions: %v", err)
	}
	if got != want {
		t.Fatalf("share session count = %d, want %d", got, want)
	}
}
