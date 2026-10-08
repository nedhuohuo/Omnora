ALTER TABLE ai_tokens
ADD COLUMN authorization_model TEXT NOT NULL DEFAULT 'legacy_boundary'
CHECK (authorization_model IN ('legacy_boundary', 'space_grant'));

CREATE TABLE ai_token_space_grants (
    token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (token_id, space_id)
);

CREATE INDEX ai_token_space_grants_space_idx
ON ai_token_space_grants(space_id, token_id);

-- SQLite requires the referenced columns to be covered by a unique key before
-- they can be the parent of the composite mount/space foreign key below.
CREATE UNIQUE INDEX mounts_id_space_unique
ON mounts(id, space_id);

CREATE TABLE ai_token_folder_denies (
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

CREATE INDEX ai_token_folder_denies_mount_idx
ON ai_token_folder_denies(token_id, space_id, mount_id, status);
