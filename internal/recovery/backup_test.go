package recovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/store"
)

func TestBackupPublisherPublishesValidatedNoReplaceArtifact(t *testing.T) {
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "live.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if _, err := db.SQL().ExecContext(context.Background(), `CREATE TABLE backup_probe(id TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO backup_probe(id, value) VALUES ('a', 'one')`); err != nil {
		t.Fatalf("insert probe: %v", err)
	}

	directory := filepath.Join(t.TempDir(), "backups")
	artifact, err := NewBackupPublisher(db).Publish(context.Background(), directory)
	if err != nil {
		t.Fatalf("publish backup: %v", err)
	}
	if artifact.ID == "" || artifact.Path == "" {
		t.Fatalf("artifact = %#v", artifact)
	}
	info, err := os.Stat(artifact.Path)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(directory, ".omnora-"+artifact.ID+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary artifact remains, err=%v", err)
	}
	readonly, err := store.OpenSQLiteReadonly(context.Background(), artifact.Path)
	if err != nil {
		t.Fatalf("open artifact readonly: %v", err)
	}
	defer readonly.Close()
	var value string
	if err := readonly.SQL().QueryRow(`SELECT value FROM backup_probe WHERE id = 'a'`).Scan(&value); err != nil {
		t.Fatalf("read artifact probe: %v", err)
	}
	if value != "one" {
		t.Fatalf("artifact value = %q, want one", value)
	}
}

func TestBackupPublisherRejectsEmptyDirectory(t *testing.T) {
	db := openRecoveryTestDB(t)
	if _, err := NewBackupPublisher(db).Publish(context.Background(), " "); err == nil {
		t.Fatal("empty directory accepted")
	}
}
