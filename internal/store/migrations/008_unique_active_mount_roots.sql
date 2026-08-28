-- Older releases could contain the same root path in more than one non-deleted
-- mount. Fail closed without touching NAS files: retire every ambiguous record
-- so the path can be discovered or registered again under one explicit owner.
CREATE TEMP TABLE migration_008_duplicate_mounts (
	id TEXT PRIMARY KEY
);

INSERT INTO migration_008_duplicate_mounts(id)
SELECT id
FROM mounts
WHERE status <> 'deleted'
  AND root_path IN (
	SELECT root_path
	FROM mounts
	WHERE status <> 'deleted'
	GROUP BY root_path
	HAVING COUNT(1) > 1
  );

UPDATE shares
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP),
	updated_at = CURRENT_TIMESTAMP
WHERE mount_id IN (SELECT id FROM migration_008_duplicate_mounts);

UPDATE upload_sessions
SET status = CASE WHEN status = 'active' THEN 'canceled' ELSE status END,
	canceled_at = CASE WHEN status = 'active' THEN COALESCE(canceled_at, CURRENT_TIMESTAMP) ELSE canceled_at END
WHERE mount_id IN (SELECT id FROM migration_008_duplicate_mounts);

DELETE FROM ai_token_boundaries
WHERE mount_id IN (SELECT id FROM migration_008_duplicate_mounts);

DELETE FROM catalog_entries
WHERE mount_id IN (SELECT id FROM migration_008_duplicate_mounts);

DELETE FROM file_objects
WHERE mount_id IN (SELECT id FROM migration_008_duplicate_mounts);

DELETE FROM mount_account_grants
WHERE mount_id IN (SELECT id FROM migration_008_duplicate_mounts);

UPDATE mounts
SET status = 'deleted',
	display_name = '__duplicate_root__' || id,
	updated_at = CURRENT_TIMESTAMP
WHERE id IN (SELECT id FROM migration_008_duplicate_mounts);

DROP TABLE migration_008_duplicate_mounts;

CREATE UNIQUE INDEX mounts_active_root_path_unique
ON mounts(root_path)
WHERE status <> 'deleted';
