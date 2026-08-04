package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func TestClaimEnforcesSingleConcurrencyAndPriority(t *testing.T) {
	db := newJobsDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "low", Kind: "index", Priority: 50}); err != nil {
		t.Fatalf("enqueue low: %v", err)
	}
	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "high", Kind: "repair", Priority: 1}); err != nil {
		t.Fatalf("enqueue high: %v", err)
	}

	claimed, err := store.Claim(ctx, "worker-a")
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed == nil || claimed.ID != "high" || claimed.Status != StatusRunning {
		t.Fatalf("claimed = %#v, want high running", claimed)
	}

	second, err := store.Claim(ctx, "worker-b")
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if second != nil {
		t.Fatalf("second claim = %#v, want nil while job is running", second)
	}

	if err := store.SaveCheckpoint(ctx, "high", `{"cursor":"file-200.txt"}`); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}
	job, err := store.Get(ctx, "high")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if job.CheckpointJSON != `{"cursor":"file-200.txt"}` {
		t.Fatalf("CheckpointJSON = %q", job.CheckpointJSON)
	}
}

func TestClaimByIDDoesNotClaimAnotherQueuedJob(t *testing.T) {
	db := newJobsDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "requested", Kind: "index", Priority: 100}); err != nil {
		t.Fatalf("enqueue requested: %v", err)
	}
	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "higher-priority", Kind: "index", Priority: 1}); err != nil {
		t.Fatalf("enqueue higher priority: %v", err)
	}

	claimed, err := store.ClaimByID(ctx, "requested", "worker-a")
	if err != nil {
		t.Fatalf("ClaimByID() error = %v", err)
	}
	if claimed == nil || claimed.ID != "requested" || claimed.Status != StatusRunning {
		t.Fatalf("claimed = %#v, want requested running", claimed)
	}

	higherPriority, err := store.Get(ctx, "higher-priority")
	if err != nil {
		t.Fatalf("get higher priority: %v", err)
	}
	if higherPriority.Status != StatusQueued || higherPriority.ClaimedBy.Valid {
		t.Fatalf("higher priority job = %#v, want queued and unclaimed", higherPriority)
	}

	second, err := store.ClaimByID(ctx, "higher-priority", "worker-b")
	if err != nil {
		t.Fatalf("second ClaimByID() error = %v", err)
	}
	if second != nil {
		t.Fatalf("second claim = %#v, want nil while job is running", second)
	}
}

func TestPauseResumeCompleteAndFailLifecycle(t *testing.T) {
	db := newJobsDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "scan", Kind: "index", Priority: 10}); err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}
	if _, err := store.Claim(ctx, "worker-a"); err != nil {
		t.Fatalf("claim scan: %v", err)
	}
	if err := store.Pause(ctx, "scan"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	paused, err := store.Get(ctx, "scan")
	if err != nil {
		t.Fatalf("get paused: %v", err)
	}
	if paused.Status != StatusPaused || paused.ClaimedBy.Valid {
		t.Fatalf("paused job = %#v, want paused and unclaimed", paused)
	}

	if err := store.Resume(ctx, "scan"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	resumed, err := store.Claim(ctx, "worker-b")
	if err != nil {
		t.Fatalf("claim resumed: %v", err)
	}
	if resumed == nil || resumed.ID != "scan" {
		t.Fatalf("resumed claim = %#v", resumed)
	}
	if err := store.Complete(ctx, "scan"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	completed, err := store.Get(ctx, "scan")
	if err != nil {
		t.Fatalf("get completed: %v", err)
	}
	if completed.Status != StatusCompleted || completed.ClaimedBy.Valid || !completed.CompletedAt.Valid {
		t.Fatalf("completed job = %#v", completed)
	}

	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "retry", Kind: "index", MaxAttempts: 2}); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}
	if _, err := store.Claim(ctx, "worker-c"); err != nil {
		t.Fatalf("claim retry: %v", err)
	}
	if err := store.Fail(ctx, "retry", errors.New("temporary")); err != nil {
		t.Fatalf("first Fail() error = %v", err)
	}
	retry, err := store.Get(ctx, "retry")
	if err != nil {
		t.Fatalf("get retry: %v", err)
	}
	if retry.Status != StatusQueued || retry.Attempts != 1 {
		t.Fatalf("retry job after first fail = %#v", retry)
	}
	if _, err := store.Claim(ctx, "worker-d"); err != nil {
		t.Fatalf("claim retry again: %v", err)
	}
	if err := store.Fail(ctx, "retry", errors.New("permanent")); err != nil {
		t.Fatalf("second Fail() error = %v", err)
	}
	failed, err := store.Get(ctx, "retry")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if failed.Status != StatusFailed || failed.Attempts != 2 || !failed.LastError.Valid {
		t.Fatalf("failed job = %#v", failed)
	}
}

func TestRequeueSavesCheckpointWithoutIncreasingAttempts(t *testing.T) {
	db := newJobsDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if _, err := store.Enqueue(ctx, EnqueueOptions{ID: "scan", Kind: "index", MaxAttempts: 2}); err != nil {
		t.Fatalf("enqueue scan: %v", err)
	}
	if _, err := store.ClaimByID(ctx, "scan", "worker-a"); err != nil {
		t.Fatalf("claim scan: %v", err)
	}
	if err := store.Requeue(ctx, "scan", `{"cursor":"file-500.txt"}`); err != nil {
		t.Fatalf("Requeue() error = %v", err)
	}

	job, err := store.Get(ctx, "scan")
	if err != nil {
		t.Fatalf("get requeued job: %v", err)
	}
	if job.Status != StatusQueued || job.Attempts != 0 || job.CheckpointJSON != `{"cursor":"file-500.txt"}` || job.ClaimedBy.Valid {
		t.Fatalf("requeued job = %#v, want queued, checkpointed, and unclaimed without attempts", job)
	}
}

func newJobsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
CREATE TABLE jobs (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 100,
	status TEXT NOT NULL DEFAULT 'queued',
	payload_json TEXT NOT NULL DEFAULT '{}',
	checkpoint_json TEXT NOT NULL DEFAULT '{}',
	attempts INTEGER NOT NULL DEFAULT 0,
	max_attempts INTEGER NOT NULL DEFAULT 3,
	claimed_at TEXT,
	claimed_by TEXT,
	last_error TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	completed_at TEXT
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}
