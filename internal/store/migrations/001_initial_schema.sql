CREATE TABLE accounts (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	display_name TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
	password_hash TEXT,
	totp_required INTEGER NOT NULL DEFAULT 0 CHECK (totp_required IN (0, 1)),
	totp_secret_ciphertext TEXT,
	totp_confirmed_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE system_state (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO system_state(key, value) VALUES
	('initialized', 'false'),
	('credential_generation', '1');

CREATE TABLE identity_initialization (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	token_hash TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	consumed_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE identity_sessions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	token_hash TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_used_at TEXT,
	revoked_at TEXT
);

CREATE TABLE browser_sessions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	token_hash TEXT NOT NULL UNIQUE,
	audience TEXT NOT NULL CHECK (audience IN ('member_web', 'admin_web')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	revoked_at TEXT
);

CREATE TABLE spaces (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN ('personal', 'shared')),
	name TEXT NOT NULL,
	owner_account_id TEXT REFERENCES accounts(id),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE space_members (
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor', 'manager')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (space_id, account_id)
);

CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
	display_name TEXT NOT NULL,
	root_path TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('managed', 'external')),
	mode TEXT NOT NULL CHECK (mode IN ('read_only', 'read_write')),
	index_enabled INTEGER NOT NULL DEFAULT 0 CHECK (index_enabled IN (0, 1)),
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'disabled', 'unavailable', 'deleted')),
	mount_identity_json TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (space_id, display_name)
);

CREATE TABLE file_objects (
	id TEXT PRIMARY KEY,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	object_kind TEXT NOT NULL CHECK (object_kind IN ('file', 'directory')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	modified_at TEXT NOT NULL,
	identity_fingerprint TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (mount_id, relative_path, identity_fingerprint)
);

CREATE TABLE shares (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	password_hash TEXT,
	creator_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	object_id TEXT REFERENCES file_objects(id) ON DELETE SET NULL,
	relative_path TEXT NOT NULL,
	allow_preview INTEGER NOT NULL DEFAULT 1 CHECK (allow_preview IN (0, 1)),
	allow_download INTEGER NOT NULL DEFAULT 1 CHECK (allow_download IN (0, 1)),
	max_visits INTEGER,
	used_visits INTEGER NOT NULL DEFAULT 0,
	max_downloads INTEGER,
	used_downloads INTEGER NOT NULL DEFAULT 0,
	expires_at TEXT NOT NULL,
	generation INTEGER NOT NULL DEFAULT 1,
	revoked_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE share_sessions (
	id TEXT PRIMARY KEY,
	share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	session_hash TEXT NOT NULL UNIQUE,
	generation INTEGER NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	revoked_at TEXT
);

CREATE TABLE ai_tokens (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	last_used_at TEXT,
	revoked_at TEXT,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_token_boundaries (
	token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	PRIMARY KEY (token_id, mount_id, relative_path)
);

CREATE TABLE upload_sessions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	target_relative_path TEXT NOT NULL,
	declared_size INTEGER NOT NULL CHECK (declared_size >= 0),
	part_size INTEGER NOT NULL CHECK (part_size > 0),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'canceled', 'expired', 'failed')),
	temp_dir TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	completed_at TEXT,
	canceled_at TEXT
);

CREATE TABLE upload_parts (
	upload_id TEXT NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
	part_number INTEGER NOT NULL CHECK (part_number > 0),
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (upload_id, part_number)
);

CREATE TABLE catalog_entries (
	id TEXT PRIMARY KEY,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	name TEXT NOT NULL,
	entry_kind TEXT NOT NULL CHECK (entry_kind IN ('file', 'directory')),
	preview_kind TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	modified_at TEXT NOT NULL,
	identity_fingerprint TEXT NOT NULL,
	indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	deleted_at TEXT,
	UNIQUE (mount_id, relative_path)
);

CREATE INDEX catalog_entries_search_idx ON catalog_entries(mount_id, name, id);
CREATE INDEX catalog_entries_space_idx ON catalog_entries(space_id, id);

CREATE TABLE jobs (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 100,
	status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'paused', 'completed', 'failed', 'canceled')),
	payload_json TEXT NOT NULL DEFAULT '{}',
	checkpoint_json TEXT NOT NULL DEFAULT '{}',
	attempts INTEGER NOT NULL DEFAULT 0,
	max_attempts INTEGER NOT NULL DEFAULT 3,
	claimed_at TEXT,
	claimed_by TEXT,
	last_error TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	completed_at TEXT
);

CREATE INDEX jobs_claim_idx ON jobs(status, priority, created_at);

CREATE TABLE route_groups (
	name TEXT PRIMARY KEY CHECK (name IN ('member_web', 'admin_web', 'share', 'rest', 'mcp', 'openapi')),
	enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
	updated_by TEXT REFERENCES accounts(id),
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	occurred_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	actor_account_id TEXT REFERENCES accounts(id),
	route_group TEXT CHECK (route_group IN ('member_web', 'admin_web', 'share', 'rest', 'mcp', 'openapi')),
	action TEXT NOT NULL,
	target_type TEXT NOT NULL,
	target_id TEXT,
	ip_hash TEXT,
	user_agent_hash TEXT,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);

INSERT OR IGNORE INTO route_groups(name, enabled) VALUES
	('member_web', 0),
	('admin_web', 0),
	('share', 0),
	('rest', 0),
	('mcp', 0),
	('openapi', 0);
