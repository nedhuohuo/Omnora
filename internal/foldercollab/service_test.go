package foldercollab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/domain"
	"omnora/internal/store"
)

func insertTestAccount(t *testing.T, ctx context.Context, db *store.DB, managed, id, email string) {
	t.Helper()
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO accounts(id, email, display_name, role, status, password_hash) VALUES (?, ?, ?, 'member', 'active', 'unused')`, id, email, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO personal_directories(account_id, relative_path, state) VALUES (?, ?, 'ready')`, id, id); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(managed, "personal", id), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsOverlappingRecipientGrants(t *testing.T) {
	ctx := context.Background()
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "folder-collab.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ownerID, recipientID := "acct_owner", "acct_recipient"
	insertTestAccount(t, ctx, db, managed, ownerID, "owner@example.com")
	insertTestAccount(t, ctx, db, managed, recipientID, "recipient@example.com")
	root := filepath.Join(managed, "personal", ownerID, "photos")
	if err := os.MkdirAll(filepath.Join(root, "2026"), 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(db.SQL(), managed)
	if _, err := svc.Create(ctx, ownerID, CreateRequest{RecipientID: recipientID, RootRelativePath: "photos", Permission: domain.ContentPermissionViewer}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, ownerID, CreateRequest{RecipientID: recipientID, RootRelativePath: "photos/2026", Permission: domain.ContentPermissionEditor})
	if !errors.Is(err, ErrOverlappingGrant) {
		t.Fatalf("overlap error = %v, want ErrOverlappingGrant", err)
	}
}

func TestCollaborationRevokeStopsListingAndOwnerCannotDelegate(t *testing.T) {
	ctx := context.Background()
	managed, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{Path: filepath.Join(t.TempDir(), "folder-collab-revoke.db"), BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ownerID, recipientID := "acct_owner2", "acct_recipient2"
	insertTestAccount(t, ctx, db, managed, ownerID, "owner2@example.com")
	insertTestAccount(t, ctx, db, managed, recipientID, "recipient2@example.com")
	root := filepath.Join(managed, "personal", ownerID, "docs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(db.SQL(), managed)
	created, err := svc.Create(ctx, ownerID, CreateRequest{RecipientID: recipientID, RootRelativePath: "docs", Permission: domain.ContentPermissionEditor})
	if err != nil {
		t.Fatal(err)
	}
	resolved, permission, err := svc.Resolve(ctx, recipientID, created.ID, "notes", true)
	if err != nil || resolved != root || permission != domain.ContentPermissionEditor {
		t.Fatalf("resolved collaboration = %q/%q, err = %v", resolved, permission, err)
	}
	if err := svc.Revoke(ctx, recipientID, created.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("recipient revoke = %v, want ErrForbidden", err)
	}
	if err := svc.Revoke(ctx, ownerID, created.ID); err != nil {
		t.Fatal(err)
	}
	items, err := svc.List(ctx, recipientID, DirectionIncoming)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("revoked collaborations = %#v", items)
	}
}
