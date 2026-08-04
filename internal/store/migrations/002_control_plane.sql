CREATE TABLE emergency_access (
	id TEXT PRIMARY KEY,
	admin_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	target_space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	reason TEXT NOT NULL,
	session_id TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	revoked_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX emergency_access_active_idx ON emergency_access(target_space_id, revoked_at, expires_at);

CREATE TABLE network_entries (
	name TEXT PRIMARY KEY CHECK (name IN ('lan_http', 'proxy_https')),
	enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
	bind_addr TEXT NOT NULL DEFAULT '',
	cidr_json TEXT NOT NULL DEFAULT '[]',
	external_https_url TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO network_entries(name, enabled) VALUES
	('lan_http', 0),
	('proxy_https', 0);

CREATE TABLE backups (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
	path TEXT,
	created_by TEXT REFERENCES accounts(id),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	completed_at TEXT,
	notes TEXT NOT NULL DEFAULT ''
);

CREATE TABLE account_preferences (
	account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
	theme TEXT NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark')),
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
