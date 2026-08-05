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
