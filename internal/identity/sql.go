package identity

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS identity_initialization (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	token_hash TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	consumed_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS identity_sessions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	token_hash TEXT NOT NULL UNIQUE,
	entry TEXT NOT NULL DEFAULT 'http',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_used_at TEXT,
	revoked_at TEXT
);
`
