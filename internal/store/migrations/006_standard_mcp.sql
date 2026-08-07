CREATE TABLE mcp_confirmations (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	ai_token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
	tool_name TEXT NOT NULL,
	args_hash TEXT NOT NULL,
	object_fingerprint TEXT NOT NULL,
	impact_json TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'declined', 'expired')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	consumed_at TEXT
);

CREATE INDEX mcp_confirmations_expiry_status_idx ON mcp_confirmations(expires_at, status);

CREATE TABLE mcp_transfer_tickets (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	ai_token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
	operation TEXT NOT NULL CHECK (operation IN ('download', 'upload')),
	required_scope TEXT NOT NULL,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	object_fingerprint TEXT NOT NULL,
	upload_id TEXT REFERENCES upload_sessions(id) ON DELETE SET NULL,
	max_bytes INTEGER NOT NULL CHECK (max_bytes >= 0),
	consumed_bytes INTEGER NOT NULL DEFAULT 0 CHECK (consumed_bytes >= 0 AND consumed_bytes <= max_bytes),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'canceled', 'expired')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	closed_at TEXT
);

CREATE INDEX mcp_transfer_tickets_expiry_status_idx ON mcp_transfer_tickets(expires_at, status);

ALTER TABLE audit_events ADD COLUMN credential_public_id TEXT;
ALTER TABLE audit_events ADD COLUMN tool_name TEXT;
ALTER TABLE audit_events ADD COLUMN result TEXT;
ALTER TABLE audit_events ADD COLUMN request_id TEXT;
ALTER TABLE audit_events ADD COLUMN trace_id TEXT;
ALTER TABLE audit_events ADD COLUMN parent_event_id INTEGER REFERENCES audit_events(id) ON DELETE SET NULL;
