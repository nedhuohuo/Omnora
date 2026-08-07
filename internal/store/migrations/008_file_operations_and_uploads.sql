ALTER TABLE upload_sessions ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE upload_sessions ADD COLUMN operation_phase TEXT;
ALTER TABLE upload_sessions ADD COLUMN lock_token TEXT;
ALTER TABLE upload_sessions ADD COLUMN lock_epoch INTEGER;
ALTER TABLE upload_sessions ADD COLUMN lease_expires_at TEXT;
ALTER TABLE upload_sessions ADD COLUMN cleanup_pending INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_pending IN (0, 1));
ALTER TABLE upload_sessions ADD COLUMN final_identity TEXT;
ALTER TABLE upload_sessions ADD COLUMN credential_generation INTEGER;

ALTER TABLE upload_parts ADD COLUMN checksum TEXT;
ALTER TABLE upload_parts ADD COLUMN state TEXT;
ALTER TABLE upload_parts ADD COLUMN write_token TEXT;

ALTER TABLE mounts ADD COLUMN canonical_root_path TEXT;
ALTER TABLE mounts ADD COLUMN identity_key TEXT;
ALTER TABLE mounts ADD COLUMN mount_source_key TEXT;

UPDATE upload_sessions
SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE credential_generation IS NULL;

CREATE TABLE file_operations (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN (
		'same_mount_rename', 'same_mount_move', 'delete', 'trash', 'trash_restore',
		'cross_mount_copy', 'cross_mount_move'
	)),
	status TEXT NOT NULL CHECK (status IN (
		'prepared', 'source_staged', 'copying', 'destination_staged',
		'published', 'source_cleaned', 'completed', 'recovery_required'
	)),
	source_space_id TEXT REFERENCES spaces(id) ON DELETE CASCADE,
	source_mount_id TEXT REFERENCES mounts(id) ON DELETE CASCADE,
	source_relative_path TEXT,
	destination_space_id TEXT REFERENCES spaces(id) ON DELETE CASCADE,
	destination_mount_id TEXT REFERENCES mounts(id) ON DELETE CASCADE,
	destination_relative_path TEXT,
	source_identity_json TEXT,
	destination_identity_json TEXT,
	manifest_json TEXT,
	lock_token TEXT,
	lock_epoch INTEGER,
	lease_expires_at TEXT,
	last_error TEXT,
	actor_account_id TEXT REFERENCES accounts(id) ON DELETE SET NULL,
	version INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	completed_at TEXT
);

CREATE INDEX file_operations_pending_idx
	ON file_operations(status, lease_expires_at, updated_at);

CREATE TABLE mount_identity_claims (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	claim_type TEXT NOT NULL CHECK (claim_type IN ('canonical_path', 'device_inode')),
	claim_key TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, claim_type),
	UNIQUE (claim_type, claim_key)
);

CREATE TABLE mount_claim_conflicts (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	conflicting_mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	reason_code TEXT NOT NULL,
	details_json TEXT NOT NULL DEFAULT '{}',
	detected_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, conflicting_mount_id, reason_code)
);

CREATE INDEX mount_claim_conflicts_lookup_idx
	ON mount_claim_conflicts(mount_id, conflicting_mount_id, detected_at);

CREATE INDEX upload_sessions_lease_cleanup_idx
	ON upload_sessions(status, lease_expires_at, cleanup_pending);
