package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"omnora/internal/access"
	"omnora/internal/catalog"
	"omnora/internal/jobs"
)

type JobWorkerOptions struct {
	WorkerID         string
	PollInterval     time.Duration
	ScheduleInterval time.Duration
}

func (s *Server) StartJobWorker(ctx context.Context, opts JobWorkerOptions) {
	workerID := strings.TrimSpace(opts.WorkerID)
	if workerID == "" {
		workerID = "omnora-worker"
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	scheduleInterval := opts.ScheduleInterval
	if scheduleInterval <= 0 {
		scheduleInterval = 15 * time.Minute
	}
	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		nextMaintenance := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if !time.Now().Before(nextMaintenance) {
				if err := s.runJobMaintenance(ctx); err != nil {
					slog.Error("background job maintenance failed", "worker", workerID, "error", err)
				}
				nextMaintenance = time.Now().Add(scheduleInterval)
			}
			worked, err := s.runNextJobBatch(ctx, workerID)
			if err != nil {
				slog.Error("background job batch failed", "worker", workerID, "error", err)
			}
			if worked {
				timer.Reset(0)
			} else {
				timer.Reset(pollInterval)
			}
		}
	}()
}

func (s *Server) runJobMaintenance(ctx context.Context) error {
	if _, err := s.enqueueCatalogScanJobs(ctx); err != nil {
		return err
	}
	return s.expireOperationalRows(ctx)
}

func (s *Server) enqueueCatalogScanJobs(ctx context.Context) (int, error) {
	db := s.sqlDB()
	if db == nil {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT payload_json
FROM jobs
WHERE kind = 'catalog_scan'
  AND status IN ('queued', 'running', 'paused')
`)
	if err != nil {
		return 0, err
	}
	activeJobs := map[string]bool{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if mountID := indexJobMountID(payload); mountID != "" {
			activeJobs[mountID] = true
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	mountRows, err := db.QueryContext(ctx, `
SELECT id
FROM mounts
WHERE status = 'active'
  AND index_enabled = 1
`)
	if err != nil {
		return 0, err
	}
	mountIDs := []string{}
	for mountRows.Next() {
		var mountID string
		if err := mountRows.Scan(&mountID); err != nil {
			_ = mountRows.Close()
			return 0, err
		}
		mountIDs = append(mountIDs, mountID)
	}
	if err := mountRows.Close(); err != nil {
		return 0, err
	}
	if err := mountRows.Err(); err != nil {
		return 0, err
	}
	store := jobs.NewStore(db)
	enqueued := 0
	for _, mountID := range mountIDs {
		if activeJobs[mountID] {
			continue
		}
		payload, _ := json.Marshal(map[string]string{"mount_id": mountID})
		if _, err := store.Enqueue(ctx, jobs.EnqueueOptions{
			Kind:        "catalog_scan",
			Priority:    100,
			PayloadJSON: string(payload),
		}); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

func (s *Server) expireOperationalRows(ctx context.Context) error {
	db := s.sqlDB()
	if db == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
UPDATE upload_sessions
SET status = 'expired'
WHERE status = 'active'
  AND expires_at <= ?
`, now); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
DELETE FROM share_sessions
WHERE expires_at <= ?
`, now)
	return err
}

func (s *Server) runNextJobBatch(ctx context.Context, workerID string) (bool, error) {
	db := s.sqlDB()
	if db == nil {
		return false, nil
	}
	store := jobs.NewStore(db)
	claimed, err := store.Claim(ctx, workerID)
	if err != nil {
		return false, err
	}
	if claimed == nil {
		return false, nil
	}
	if claimed.Kind != "catalog_scan" {
		err := fmt.Errorf("unsupported job kind %q", claimed.Kind)
		_ = store.Fail(ctx, claimed.ID, err)
		return true, err
	}
	if _, err := s.runCatalogScanBatch(ctx, store, *claimed); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Server) runCatalogScanBatch(ctx context.Context, store jobs.Store, job jobs.Job) (catalog.ScanResult, error) {
	var payload struct {
		MountID string `json:"mount_id"`
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.MountID == "" {
		cause := fmt.Errorf("job payload is invalid")
		_ = store.Fail(ctx, job.ID, cause)
		return catalog.ScanResult{}, cause
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://omnora.local/_worker/jobs/"+job.ID, nil)
	if err != nil {
		_ = store.Fail(ctx, job.ID, err)
		return catalog.ScanResult{}, err
	}
	mount, err := s.loadCatalogMount(request, payload.MountID)
	if err != nil {
		_ = store.Fail(ctx, job.ID, err)
		return catalog.ScanResult{}, err
	}
	if err := s.guard.VerifyMountIdentity(ctx, access.AuthorizedMount{ID: mount.ID, Root: mount.Root, MountRoot: mount.Root, IdentityJSON: mount.IdentityJSON}); err != nil {
		_ = store.Fail(ctx, job.ID, err)
		return catalog.ScanResult{}, err
	}
	var checkpoint struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(job.CheckpointJSON), &checkpoint); err != nil {
		_ = store.Fail(ctx, job.ID, err)
		return catalog.ScanResult{}, err
	}
	result, err := catalog.NewService(s.sqlDB()).ScanBatch(ctx, mount, catalog.ScanOptions{
		BatchSize: catalog.DefaultBatchSize,
		Cursor:    checkpoint.Cursor,
	})
	if err != nil {
		_ = store.Fail(ctx, job.ID, err)
		return catalog.ScanResult{}, err
	}
	if !result.Done {
		checkpoint, _ := json.Marshal(map[string]string{"cursor": result.NextCursor})
		if err := store.Requeue(ctx, job.ID, string(checkpoint)); err != nil {
			return catalog.ScanResult{}, err
		}
		return result, nil
	}
	if err := store.Complete(ctx, job.ID); err != nil {
		return catalog.ScanResult{}, err
	}
	return result, nil
}
