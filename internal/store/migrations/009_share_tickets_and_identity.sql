ALTER TABLE shares ADD COLUMN target_kind TEXT;
ALTER TABLE shares ADD COLUMN target_identity_json TEXT;
ALTER TABLE shares ADD COLUMN invalidated_at TEXT;
ALTER TABLE shares ADD COLUMN invalidated_reason TEXT;
ALTER TABLE shares ADD COLUMN credential_generation INTEGER;
ALTER TABLE share_sessions ADD COLUMN credential_generation INTEGER;

UPDATE shares
SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE credential_generation IS NULL;

UPDATE share_sessions
SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE credential_generation IS NULL;

CREATE TABLE share_download_tickets (
	id TEXT PRIMARY KEY,
	secret_hash TEXT NOT NULL UNIQUE,
	share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	share_session_id TEXT NOT NULL REFERENCES share_sessions(id) ON DELETE CASCADE,
	generation INTEGER NOT NULL CHECK (generation >= 0),
	credential_generation INTEGER NOT NULL,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	etag TEXT NOT NULL,
	object_identity_json TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	status TEXT NOT NULL CHECK (status IN ('issued', 'streaming', 'completed', 'expired', 'canceled')),
	transfer_owner TEXT,
	lease_expires_at TEXT,
	committed_offset INTEGER NOT NULL DEFAULT 0 CHECK (committed_offset >= 0 AND committed_offset <= size_bytes),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	completed_at TEXT,
	canceled_at TEXT
);

CREATE INDEX share_download_tickets_status_expiry_idx
	ON share_download_tickets(status, expires_at);
CREATE INDEX share_download_tickets_share_generation_idx
	ON share_download_tickets(share_id, generation);
CREATE INDEX share_download_tickets_session_status_idx
	ON share_download_tickets(share_session_id, status);
