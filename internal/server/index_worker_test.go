package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/jobs"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

func TestRunNextJobBatchConsumesQueuedCatalogScan(t *testing.T) {
	skipLegacySpaceRESTTest(t)
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "index-worker-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := NewServer(config.Config{
		Routes: map[domain.RouteGroup]bool{
			domain.RouteGroupREST: true,
		},
	}, db)
	admin, _ := createAPITestAccounts(t, db)

	workspace, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	root, err := os.MkdirTemp(workspace, ".index-worker-")
	if err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for i := range 501 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d.txt", i)), []byte("content"), 0o600); err != nil {
			t.Fatalf("write indexed file: %v", err)
		}
	}
	identity, err := mountid.Capture(root)
	if err != nil {
		t.Fatalf("capture mount identity: %v", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("marshal mount identity: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('worker-space', 'shared', 'Worker Space', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES ('worker-mount', 'worker-space', 'Worker Mount', ?, 'external', 'read_only', 1, 'active', ?)
`, root, string(identityJSON)); err != nil {
		t.Fatalf("insert mount: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"mount_id": "worker-mount"})
	job, err := jobs.NewStore(db.SQL()).Enqueue(ctx, jobs.EnqueueOptions{
		ID:          "worker-job",
		Kind:        "catalog_scan",
		PayloadJSON: string(payload),
	})
	if err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	worked, err := srv.runNextJobBatch(ctx, "test-worker")
	if err != nil {
		t.Fatalf("first runNextJobBatch() error = %v", err)
	}
	if !worked {
		t.Fatal("first runNextJobBatch() worked = false, want true")
	}
	requeued, err := jobs.NewStore(db.SQL()).Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get requeued job: %v", err)
	}
	if requeued.Status != jobs.StatusQueued || requeued.CheckpointJSON == "{}" {
		t.Fatalf("requeued job = %#v, want queued with checkpoint", requeued)
	}

	worked, err = srv.runNextJobBatch(ctx, "test-worker")
	if err != nil {
		t.Fatalf("second runNextJobBatch() error = %v", err)
	}
	if !worked {
		t.Fatal("second runNextJobBatch() worked = false, want true")
	}
	completed, err := jobs.NewStore(db.SQL()).Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	if completed.Status != jobs.StatusCompleted {
		t.Fatalf("completed job status = %q, want completed", completed.Status)
	}
	var entries int
	if err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(1) FROM catalog_entries WHERE mount_id = 'worker-mount'").Scan(&entries); err != nil {
		t.Fatalf("count catalog entries: %v", err)
	}
	if entries != 501 {
		t.Fatalf("catalog entry count = %d, want 501", entries)
	}
}

func TestCatalogSchedulerEnqueuesActiveIndexedMountsOnce(t *testing.T) {
	skipLegacySpaceRESTTest(t)
	ctx := context.Background()
	db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "index-scheduler-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := NewServer(config.Config{Routes: map[domain.RouteGroup]bool{}}, db)
	admin, _ := createAPITestAccounts(t, db)
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO spaces(id, kind, name, owner_account_id, status)
VALUES ('scheduler-space', 'shared', 'Scheduler Space', ?, 'active')
`, admin.ID); err != nil {
		t.Fatalf("insert space: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status)
VALUES
	('indexed-active', 'scheduler-space', 'Indexed', '/srv/indexed', 'external', 'read_only', 1, 'active'),
	('indexed-disabled', 'scheduler-space', 'Disabled', '/srv/disabled', 'external', 'read_only', 1, 'disabled'),
	('not-indexed', 'scheduler-space', 'No Index', '/srv/no-index', 'external', 'read_only', 0, 'active')
`); err != nil {
		t.Fatalf("insert mounts: %v", err)
	}

	enqueued, err := srv.enqueueCatalogScanJobs(ctx)
	if err != nil {
		t.Fatalf("enqueueCatalogScanJobs() error = %v", err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueued = %d, want 1", enqueued)
	}
	enqueued, err = srv.enqueueCatalogScanJobs(ctx)
	if err != nil {
		t.Fatalf("second enqueueCatalogScanJobs() error = %v", err)
	}
	if enqueued != 0 {
		t.Fatalf("second enqueued = %d, want 0", enqueued)
	}
	var jobsCount int
	if err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(1) FROM jobs WHERE kind = 'catalog_scan'").Scan(&jobsCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobsCount != 1 {
		t.Fatalf("jobs count = %d, want 1", jobsCount)
	}
}
