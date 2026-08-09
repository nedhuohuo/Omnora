-- This is an intentionally destructive, offline-only control-plane reset.
-- Omnora has not been released, so legacy Space-scoped metadata is not
-- carried forward. No statement in this migration touches the filesystem.

DROP TABLE mcp_transfer_tickets;
DROP TABLE share_download_tickets;
DROP TABLE share_sessions;
DROP TABLE shares;
DROP TABLE upload_parts;
DROP TABLE upload_sessions;
DROP TABLE file_operations;
DROP TABLE mount_claim_conflicts;
DROP TABLE mount_identity_claims;
DROP TABLE catalog_entries;
DROP TABLE file_objects;
DROP TABLE ai_token_boundaries;
DROP TABLE space_members;
DROP TABLE mounts;
DROP TABLE spaces;

-- Old jobs and confirmations can embed Space-era object coordinates in
-- opaque JSON/text. They are control-plane state, not physical content.
DELETE FROM jobs;
DELETE FROM mcp_confirmations;
UPDATE ai_tokens
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP),
	updated_at = CURRENT_TIMESTAMP;

CREATE TABLE mounts (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL UNIQUE,
	root_path TEXT NOT NULL UNIQUE,
	purpose TEXT NOT NULL CHECK (purpose IN ('personal_default', 'common')),
	storage_kind TEXT NOT NULL CHECK (storage_kind IN ('managed', 'external')),
	governance TEXT NOT NULL CHECK (governance IN ('system', 'normal', 'restricted')),
	mode TEXT NOT NULL CHECK (mode IN ('read_only', 'read_write')),
	index_enabled INTEGER NOT NULL DEFAULT 0 CHECK (index_enabled IN (0, 1)),
	share_enabled INTEGER NOT NULL DEFAULT 1 CHECK (share_enabled IN (0, 1)),
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'disabled', 'unavailable', 'deleted')),
	mount_identity_json TEXT,
	canonical_root_path TEXT,
	identity_key TEXT,
	mount_source_key TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CHECK (
		(purpose = 'personal_default' AND storage_kind = 'managed' AND governance = 'system' AND mode = 'read_write' AND share_enabled = 1 AND status IN ('active', 'unavailable'))
		OR
		(purpose = 'common' AND storage_kind = 'external' AND governance IN ('normal', 'restricted'))
	)
);

CREATE UNIQUE INDEX mounts_single_personal_default_idx
	ON mounts(purpose) WHERE purpose = 'personal_default';

INSERT INTO mounts(
	id, display_name, root_path, purpose, storage_kind, governance,
	mode, index_enabled, status
) VALUES (
	'personal-default', 'Personal Files', 'personal', 'personal_default',
	'managed', 'system', 'read_write', 1, 'active'
);

CREATE TABLE personal_directories (
	account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
	relative_path TEXT NOT NULL UNIQUE CHECK (relative_path <> '' AND relative_path = account_id),
	state TEXT NOT NULL CHECK (state IN ('ready', 'retained')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO personal_directories(account_id, relative_path, state)
SELECT id, id, CASE WHEN status = 'deleted' THEN 'retained' ELSE 'ready' END
FROM accounts;

CREATE TRIGGER accounts_sync_personal_directory_state
AFTER UPDATE OF status ON accounts
WHEN OLD.status <> NEW.status
BEGIN
	UPDATE personal_directories
	SET state = CASE WHEN NEW.status = 'deleted' THEN 'retained' ELSE 'ready' END,
		updated_at = CURRENT_TIMESTAMP
	WHERE account_id = NEW.id;
END;

CREATE TABLE mount_grants (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, account_id)
);

CREATE TRIGGER mount_grants_common_mount_insert
BEFORE INSERT ON mount_grants
WHEN NOT EXISTS (
	SELECT 1 FROM mounts WHERE id = NEW.mount_id AND purpose = 'common'
)
BEGIN
	SELECT RAISE(ABORT, 'mount grants require a common mount');
END;

CREATE TABLE ai_token_boundaries (
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

CREATE UNIQUE INDEX ai_token_boundaries_account_source_idx
	ON ai_token_boundaries(token_id, source, relative_path)
	WHERE mount_id IS NULL;
CREATE UNIQUE INDEX ai_token_boundaries_common_mount_idx
	ON ai_token_boundaries(token_id, mount_id, relative_path)
	WHERE source = 'common_mount';

CREATE TRIGGER mount_grants_common_mount_update
BEFORE UPDATE OF mount_id ON mount_grants
WHEN NOT EXISTS (
	SELECT 1 FROM mounts WHERE id = NEW.mount_id AND purpose = 'common'
)
BEGIN
	SELECT RAISE(ABORT, 'mount grants require a common mount');
END;

CREATE TABLE folder_collaborations (
	id TEXT PRIMARY KEY,
	owner_account_id TEXT NOT NULL REFERENCES personal_directories(account_id) ON DELETE CASCADE,
	recipient_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	root_relative_path TEXT NOT NULL,
	root_identity_json TEXT NOT NULL,
	permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
	created_by_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	revoked_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CHECK (owner_account_id <> recipient_account_id),
	CHECK (owner_account_id = created_by_account_id)
);

CREATE UNIQUE INDEX folder_collaborations_active_exact_root_idx
	ON folder_collaborations(owner_account_id, recipient_account_id, root_relative_path)
	WHERE revoked_at IS NULL;
CREATE INDEX folder_collaborations_recipient_active_idx
	ON folder_collaborations(recipient_account_id, revoked_at, id);

CREATE TABLE file_objects (
	id TEXT PRIMARY KEY,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	object_kind TEXT NOT NULL CHECK (object_kind IN ('file', 'directory')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	modified_at TEXT NOT NULL,
	identity_fingerprint TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (mount_id, relative_path, identity_fingerprint)
);

CREATE TABLE shares (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	password_hash TEXT,
	creator_account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	object_id TEXT REFERENCES file_objects(id) ON DELETE SET NULL,
	relative_path TEXT NOT NULL,
	allow_preview INTEGER NOT NULL DEFAULT 1 CHECK (allow_preview IN (0, 1)),
	allow_download INTEGER NOT NULL DEFAULT 1 CHECK (allow_download IN (0, 1)),
	max_visits INTEGER,
	used_visits INTEGER NOT NULL DEFAULT 0,
	max_downloads INTEGER,
	used_downloads INTEGER NOT NULL DEFAULT 0,
	expires_at TEXT NOT NULL,
	generation INTEGER NOT NULL DEFAULT 1,
	revoked_at TEXT,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	fragment_secret TEXT,
	target_kind TEXT,
	target_identity_json TEXT,
	invalidated_at TEXT,
	invalidated_reason TEXT,
	credential_generation INTEGER
);

CREATE TABLE share_sessions (
	id TEXT PRIMARY KEY,
	share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	session_hash TEXT NOT NULL UNIQUE,
	generation INTEGER NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	revoked_at TEXT,
	credential_generation INTEGER
);

CREATE TABLE share_download_tickets (
	id TEXT PRIMARY KEY,
	secret_hash TEXT NOT NULL UNIQUE,
	share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	share_session_id TEXT NOT NULL REFERENCES share_sessions(id) ON DELETE CASCADE,
	generation INTEGER NOT NULL CHECK (generation >= 0),
	credential_generation INTEGER NOT NULL,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	etag TEXT NOT NULL,
	object_identity_json TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	status TEXT NOT NULL CHECK (status IN ('issued', 'streaming', 'completed', 'expired', 'canceled')),
	transfer_owner TEXT,
	lease_expires_at TEXT,
	committed_offset INTEGER NOT NULL DEFAULT 0 CHECK (committed_offset >= 0 AND committed_offset <= size_bytes),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	completed_at TEXT,
	canceled_at TEXT
);

CREATE INDEX share_download_tickets_status_expiry_idx
	ON share_download_tickets(status, expires_at);
CREATE INDEX share_download_tickets_share_generation_idx
	ON share_download_tickets(share_id, generation);
CREATE INDEX share_download_tickets_session_status_idx
	ON share_download_tickets(share_session_id, status);

CREATE TABLE upload_sessions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	target_relative_path TEXT NOT NULL,
	declared_size INTEGER NOT NULL CHECK (declared_size >= 0),
	part_size INTEGER NOT NULL CHECK (part_size > 0),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'canceled', 'expired', 'failed')),
	temp_dir TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	completed_at TEXT,
	canceled_at TEXT,
	version INTEGER NOT NULL DEFAULT 1,
	operation_phase TEXT,
	lock_token TEXT,
	lock_epoch INTEGER,
	lease_expires_at TEXT,
	cleanup_pending INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_pending IN (0, 1)),
	final_identity TEXT,
	credential_generation INTEGER,
	expected_target_identity TEXT
);

CREATE TABLE upload_parts (
	upload_id TEXT NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
	part_number INTEGER NOT NULL CHECK (part_number > 0),
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	checksum TEXT,
	state TEXT,
	write_token TEXT,
	PRIMARY KEY (upload_id, part_number)
);

CREATE INDEX upload_sessions_lease_cleanup_idx
	ON upload_sessions(status, lease_expires_at, cleanup_pending);

CREATE TABLE catalog_entries (
	id TEXT PRIMARY KEY,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	name TEXT NOT NULL,
	entry_kind TEXT NOT NULL CHECK (entry_kind IN ('file', 'directory')),
	preview_kind TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	modified_at TEXT NOT NULL,
	identity_fingerprint TEXT NOT NULL,
	indexed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	deleted_at TEXT,
	last_seen_scan_id TEXT,
	UNIQUE (mount_id, relative_path)
);

CREATE INDEX catalog_entries_search_idx ON catalog_entries(mount_id, name, id);
CREATE INDEX catalog_entries_scan_epoch_idx
	ON catalog_entries(mount_id, last_seen_scan_id, deleted_at);

CREATE TABLE file_operations (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN (
		'same_mount_rename', 'same_mount_move', 'delete', 'trash', 'trash_restore',
		'trash_purge', 'trash_empty',
		'cross_mount_copy', 'cross_mount_move'
	)),
	status TEXT NOT NULL CHECK (status IN (
		'prepared', 'source_staged', 'copying', 'destination_staged',
		'published', 'source_cleaned', 'completed', 'recovery_required'
	)),
	source_mount_id TEXT REFERENCES mounts(id) ON DELETE CASCADE,
	source_relative_path TEXT,
	destination_mount_id TEXT REFERENCES mounts(id) ON DELETE CASCADE,
	destination_relative_path TEXT,
	source_identity_json TEXT,
	destination_identity_json TEXT,
	manifest_json TEXT,
	lock_token TEXT,
	lock_epoch INTEGER,
	lease_expires_at TEXT,
	last_error TEXT,
	actor_account_id TEXT REFERENCES accounts(id) ON DELETE SET NULL,
	version INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	completed_at TEXT
);

CREATE INDEX file_operations_pending_idx
	ON file_operations(status, lease_expires_at, updated_at);

CREATE TABLE mount_identity_claims (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	claim_type TEXT NOT NULL CHECK (claim_type IN ('canonical_path', 'device_inode')),
	claim_key TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, claim_type),
	UNIQUE (claim_type, claim_key)
);

CREATE TABLE mount_claim_conflicts (
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	conflicting_mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	reason_code TEXT NOT NULL,
	details_json TEXT NOT NULL DEFAULT '{}',
	detected_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (mount_id, conflicting_mount_id, reason_code)
);

CREATE INDEX mount_claim_conflicts_lookup_idx
	ON mount_claim_conflicts(mount_id, conflicting_mount_id, detected_at);

CREATE TABLE mcp_transfer_tickets (
	id TEXT PRIMARY KEY,
	public_id TEXT NOT NULL UNIQUE,
	secret_hash TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	ai_token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
	operation TEXT NOT NULL CHECK (operation IN ('download', 'upload')),
	required_scope TEXT NOT NULL,
	mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	object_fingerprint TEXT NOT NULL,
	upload_id TEXT REFERENCES upload_sessions(id) ON DELETE SET NULL,
	max_bytes INTEGER NOT NULL CHECK (max_bytes >= 0),
	consumed_bytes INTEGER NOT NULL DEFAULT 0 CHECK (consumed_bytes >= 0 AND consumed_bytes <= max_bytes),
	status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'canceled', 'expired')),
	created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TEXT NOT NULL,
	closed_at TEXT
);

CREATE INDEX mcp_transfer_tickets_expiry_status_idx
	ON mcp_transfer_tickets(expires_at, status);

CREATE TRIGGER mounts_protect_default_delete
BEFORE DELETE ON mounts
WHEN OLD.purpose = 'personal_default'
BEGIN
	SELECT RAISE(ABORT, 'personal default mount is protected');
END;

CREATE TRIGGER mounts_protect_classification_update
BEFORE UPDATE OF root_path, purpose, storage_kind, governance ON mounts
WHEN OLD.root_path <> NEW.root_path
	OR OLD.purpose <> NEW.purpose
	OR OLD.storage_kind <> NEW.storage_kind
	OR OLD.governance <> NEW.governance
BEGIN
	SELECT RAISE(ABORT, 'mount classification is immutable');
END;

CREATE TRIGGER mounts_protect_default_status
BEFORE UPDATE OF status ON mounts
WHEN OLD.purpose = 'personal_default' AND NEW.status NOT IN ('active', 'unavailable')
BEGIN
	SELECT RAISE(ABORT, 'personal default mount status is protected');
END;
