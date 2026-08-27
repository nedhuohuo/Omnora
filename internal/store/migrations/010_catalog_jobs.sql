ALTER TABLE catalog_entries ADD COLUMN last_seen_scan_id TEXT;

CREATE INDEX catalog_entries_scan_epoch_idx
	ON catalog_entries(mount_id, last_seen_scan_id, deleted_at);

ALTER TABLE jobs ADD COLUMN claim_token TEXT;
ALTER TABLE jobs ADD COLUMN lease_expires_at TEXT;
ALTER TABLE jobs ADD COLUMN heartbeat_at TEXT;

CREATE INDEX jobs_lease_claim_idx
	ON jobs(status, lease_expires_at, priority, created_at, id);
