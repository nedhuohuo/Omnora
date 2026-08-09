package identity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/domain"
	"omnora/internal/store"
)

func TestCreateAccountCreatesStablePersonalDirectory(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "identity-target.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db.SQL(), Options{ManagedDir: managed})

	created, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email: "person@example.com", DisplayName: "Person", Password: "CorrectHorse1!", Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if created.PersonalDirectory.AccountID != created.Account.ID || created.PersonalDirectory.RelativePath != created.Account.ID || created.PersonalDirectory.State != "ready" {
		t.Fatalf("personal directory = %#v, account=%q", created.PersonalDirectory, created.Account.ID)
	}
	physical := filepath.Join(managed, "personal", created.Account.ID)
	if info, err := os.Stat(physical); err != nil || !info.IsDir() {
		t.Fatalf("personal directory %q: info=%v error=%v", physical, info, err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM mounts WHERE purpose = 'personal_default'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("default mount count=%d error=%v", count, err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM personal_directories WHERE account_id = ? AND relative_path = ? AND state = 'ready'`, created.Account.ID, created.Account.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("personal directory row count=%d error=%v", count, err)
	}
}

func TestDeleteAccountRetainsPersonalDirectoryAndRevokesAccess(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "identity-delete.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db.SQL(), Options{ManagedDir: managed})
	created, err := svc.CreateAccount(ctx, CreateAccountRequest{
		Email: "deleted@example.com", DisplayName: "Deleted", Password: "CorrectHorse1!", Role: domain.AccountRoleMember,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := svc.CreateSession(ctx, SessionRequest{AccountID: created.Account.ID, TTL: time.Hour, Entry: DefaultSessionEntry, Purpose: SessionPurposeFull})
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(managed, "personal", created.Account.ID)
	keptFile := filepath.Join(physical, "keep.txt")
	if err := os.WriteFile(keptFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteAccountSecure(ctx, created.Account.ID, nil); err != nil {
		t.Fatalf("DeleteAccountSecure() error = %v", err)
	}
	if _, err := os.Stat(keptFile); err != nil {
		t.Fatalf("deleted account file was removed: %v", err)
	}
	var status, state, relativePath string
	if err := db.SQL().QueryRow(`
SELECT a.status, pd.state, pd.relative_path
FROM accounts a JOIN personal_directories pd ON pd.account_id = a.id
WHERE a.id = ?
`, created.Account.ID).Scan(&status, &state, &relativePath); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || state != "retained" || relativePath != created.Account.ID {
		t.Fatalf("retained identity = status:%q state:%q path:%q", status, state, relativePath)
	}
	if _, err := svc.VerifySession(ctx, issued.Token); err == nil {
		t.Fatal("deleted account session remained valid")
	}
}
