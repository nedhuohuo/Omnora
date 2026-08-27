ALTER TABLE route_groups ADD COLUMN configured INTEGER NOT NULL DEFAULT 0 CHECK (configured IN (0, 1));

UPDATE route_groups
SET configured = 1
WHERE EXISTS (SELECT 1 FROM accounts);

CREATE TABLE recovery_control (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	state TEXT NOT NULL CHECK (state IN (
		'normal', 'preparing', 'recovery_required', 'restoring',
		'finalize_required', 'normal_pending_bootstrap'
	)),
	ready INTEGER NOT NULL DEFAULT 0 CHECK (ready IN (0, 1)),
	request_id TEXT,
	staging_path TEXT,
	source_schema_version INTEGER,
	safe_snapshot_path TEXT,
	reason_code TEXT,
	cleanup_pending INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_pending IN (0, 1)),
	requested_at TEXT,
	completed_at TEXT,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO recovery_control(id, state, ready)
VALUES (1, 'normal', 0);

CREATE TABLE restore_requests (
	id TEXT PRIMARY KEY,
	backup_id TEXT NOT NULL REFERENCES backups(id) ON DELETE RESTRICT,
	state TEXT NOT NULL CHECK (state IN (
		'preparing', 'recovery_required', 'restoring',
		'finalize_required', 'normal_pending_bootstrap', 'completed', 'failed'
	)),
	staging_path TEXT,
	source_schema_version INTEGER,
	safe_snapshot_path TEXT,
	reason_code TEXT,
	cleanup_pending INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_pending IN (0, 1)),
	requested_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	completed_at TEXT
);

CREATE INDEX restore_requests_state_idx
	ON restore_requests(state, requested_at);
CREATE INDEX restore_requests_cleanup_idx
	ON restore_requests(cleanup_pending, requested_at);
