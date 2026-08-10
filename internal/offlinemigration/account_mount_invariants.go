package offlinemigration

import (
	"context"
	"database/sql"
	"errors"
)

// AccountMountInvariants are the mandatory post-013 domain checks. They are
// intentionally expressed as count-only queries so validation errors never
// expose account, mount, or path values.
func AccountMountInvariants() []DomainInvariant {
	return []DomainInvariant{
		{Name: "legacy schema absent", Check: rejectLegacySchema},
		{Name: "single protected personal default mount", Check: requirePersonalDefaultMount},
		{Name: "mount classifications", Check: requireMountClassifications},
		{Name: "one stable personal directory per account", Check: requirePersonalDirectories},
		{Name: "common mount grants", Check: requireMountGrants},
		{Name: "folder collaboration permissions", Check: requireFolderCollaborations},
	}
}

func rejectLegacySchema(ctx context.Context, db *sql.DB) error {
	return requireZero(ctx, db, `
SELECT
  (SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name IN ('spaces', 'space_members')) +
  (SELECT COUNT(*)
     FROM sqlite_schema AS schema_object,
          pragma_table_info(schema_object.name) AS column_info
    WHERE schema_object.type = 'table' AND column_info.name LIKE '%space_id%')`)
}

func requirePersonalDefaultMount(ctx context.Context, db *sql.DB) error {
	return requireZero(ctx, db, `
SELECT CASE WHEN COUNT(*) = 1 THEN 0 ELSE 1 END
FROM mounts
WHERE id = 'personal-default'
  AND root_path = 'personal'
  AND purpose = 'personal_default'
  AND storage_kind = 'managed'
  AND governance = 'system'
  AND mode = 'read_write'
  AND status = 'active'`)
}

func requireMountClassifications(ctx context.Context, db *sql.DB) error {
	return requireZero(ctx, db, `
SELECT COUNT(*)
FROM mounts
WHERE NOT (
  purpose = 'personal_default' AND storage_kind = 'managed' AND governance = 'system'
  AND mode = 'read_write' AND status = 'active'
) AND NOT (
  purpose = 'common' AND storage_kind = 'external' AND governance IN ('normal', 'restricted')
)`)
}

func requirePersonalDirectories(ctx context.Context, db *sql.DB) error {
	return requireZero(ctx, db, `
SELECT
  (SELECT COUNT(*)
     FROM accounts AS account
     LEFT JOIN personal_directories AS directory ON directory.account_id = account.id
    WHERE directory.account_id IS NULL) +
  (SELECT COUNT(*)
     FROM personal_directories AS directory
     LEFT JOIN accounts AS account ON account.id = directory.account_id
    WHERE account.id IS NULL
       OR directory.relative_path <> directory.account_id
       OR directory.state <> CASE WHEN account.status = 'deleted' THEN 'retained' ELSE 'ready' END)`)
}

func requireMountGrants(ctx context.Context, db *sql.DB) error {
	return requireZero(ctx, db, `
SELECT COUNT(*)
FROM mount_grants AS grant_row
LEFT JOIN mounts AS mount ON mount.id = grant_row.mount_id
WHERE mount.id IS NULL
   OR mount.purpose <> 'common'
   OR grant_row.permission NOT IN ('viewer', 'editor')`)
}

func requireFolderCollaborations(ctx context.Context, db *sql.DB) error {
	return requireZero(ctx, db, `
SELECT COUNT(*)
FROM folder_collaborations
WHERE owner_account_id = recipient_account_id
   OR permission NOT IN ('viewer', 'editor')`)
}

func requireZero(ctx context.Context, db *sql.DB, query string) error {
	var violations int
	if err := db.QueryRowContext(ctx, query).Scan(&violations); err != nil {
		return errors.New("domain invariant query failed")
	}
	if violations != 0 {
		return errors.New("domain invariant violation")
	}
	return nil
}
