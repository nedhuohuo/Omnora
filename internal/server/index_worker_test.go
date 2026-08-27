package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/jobs"
	"omnora/internal/store"
)

func TestRunNextJobBatchIndexesPersonalDefaultMount(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "personal-index-worker-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	managedDir, err := os.MkdirTemp(workspace, ".personal-index-worker-")
	if err != nil {
		t.Fatalf("create managed directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(managedDir) })
	srv := NewServer(config.Config{
		Storage: config.StorageConfig{ManagedDir: managedDir},
		Routes:  map[domain.RouteGroup]bool{},
	}, db)
	personalRoot := filepath.Join(managedDir, "personal")
	if err := os.WriteFile(filepath.Join(personalRoot, "indexed.txt"), []byte("personal content"), 0o600); err != nil {
		t.Fatalf("write personal file: %v", err)
	}

	payload, _ := json.Marshal(map[string]string{"mount_id": "personal-default"})
	job, err := jobs.NewStore(db.SQL()).Enqueue(ctx, jobs.EnqueueOptions{
		ID:          "personal-worker-job",
		Kind:        "catalog_scan",
		PayloadJSON: string(payload),
	})
	if err != nil {
		t.Fatalf("enqueue personal catalog job: %v", err)
	}

	worked, err := srv.runNextJobBatch(ctx, "test-worker")
	if err != nil {
		t.Fatalf("runNextJobBatch() error = %v", err)
	}
	if !worked {
		t.Fatal("runNextJobBatch() worked = false, want true")
	}
	completed, err := jobs.NewStore(db.SQL()).Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	if completed.Status != jobs.StatusCompleted {
		t.Fatalf("completed job status = %q, want completed", completed.Status)
	}
	var entries int
	if err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(1) FROM catalog_entries WHERE mount_id = 'personal-default'").Scan(&entries); err != nil {
		t.Fatalf("count personal catalog entries: %v", err)
	}
	if entries != 1 {
		t.Fatalf("personal catalog entries = %d, want 1", entries)
	}
}
