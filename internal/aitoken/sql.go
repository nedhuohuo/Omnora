package aitoken

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS ai_tokens (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	scopes TEXT NOT NULL,
	authorization_model TEXT NOT NULL DEFAULT 'legacy_boundary'
		CHECK (authorization_model IN ('legacy_boundary', 'space_grant')),
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

	CREATE TABLE IF NOT EXISTS ai_token_space_grants (
		token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
		space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
		permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (token_id, space_id)
	);

	CREATE INDEX IF NOT EXISTS ai_token_space_grants_space_idx
	ON ai_token_space_grants(space_id, token_id);

	CREATE UNIQUE INDEX IF NOT EXISTS mounts_id_space_unique
	ON mounts(id, space_id);

	CREATE TABLE IF NOT EXISTS ai_token_folder_denies (
		token_id TEXT NOT NULL,
		space_id TEXT NOT NULL,
		mount_id TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		identity_fingerprint TEXT NOT NULL CHECK (length(trim(identity_fingerprint)) > 0),
		status TEXT NOT NULL DEFAULT 'active'
			CHECK (status IN ('active', 'needs_review')),
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (token_id, mount_id, relative_path),
		FOREIGN KEY (token_id, space_id)
			REFERENCES ai_token_space_grants(token_id, space_id) ON DELETE CASCADE,
		FOREIGN KEY (mount_id, space_id)
			REFERENCES mounts(id, space_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS ai_token_folder_denies_mount_idx
	ON ai_token_folder_denies(token_id, space_id, mount_id, status);
	`
