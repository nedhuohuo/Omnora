ALTER TABLE mounts
ADD COLUMN allow_public_shares INTEGER NOT NULL DEFAULT 0
CHECK (allow_public_shares IN (0, 1));

UPDATE mounts
SET allow_public_shares = 1
WHERE status <> 'deleted';

CREATE TABLE mount_account_grants (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor', 'manager')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, account_id)
);

CREATE INDEX mount_account_grants_account_idx ON mount_account_grants(account_id, mount_id);

-- Preserve the effective access existing installations had before mount grants.
INSERT INTO mount_account_grants(mount_id, account_id, permission)
SELECT m.id, sm.account_id, sm.permission
FROM mounts m
JOIN space_members sm ON sm.space_id = m.space_id
WHERE m.status <> 'deleted';
