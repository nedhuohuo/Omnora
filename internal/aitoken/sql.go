package aitoken

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS ai_tokens (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	credential_generation INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	last_used_at TEXT,
	revoked_at TEXT,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ai_token_boundaries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
	source TEXT NOT NULL CHECK (source IN ('personal', 'common_mount', 'all_account_content')),
	mount_id TEXT REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CHECK (
		(source = 'common_mount' AND mount_id IS NOT NULL)
		OR
		(source IN ('personal', 'all_account_content') AND mount_id IS NULL)
	),
	CHECK (source <> 'all_account_content' OR relative_path = '')
);

CREATE UNIQUE INDEX IF NOT EXISTS ai_token_boundaries_account_source_idx
	ON ai_token_boundaries(token_id, source, relative_path)
	WHERE mount_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ai_token_boundaries_common_mount_idx
	ON ai_token_boundaries(token_id, mount_id, relative_path)
	WHERE source = 'common_mount';
`
