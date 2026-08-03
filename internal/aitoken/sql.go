package aitoken

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS ai_tokens (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_used_at TEXT,
	revoked_at TEXT,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ai_token_boundaries (
	token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
	space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	PRIMARY KEY (token_id, mount_id, relative_path)
);
`
