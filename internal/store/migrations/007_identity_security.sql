ALTER TABLE accounts ADD COLUMN totp_pending_secret_ciphertext TEXT;
ALTER TABLE accounts ADD COLUMN totp_pending_expires_at TEXT;
ALTER TABLE accounts ADD COLUMN password_reset_required INTEGER NOT NULL DEFAULT 0 CHECK (password_reset_required IN (0, 1));
ALTER TABLE accounts ADD COLUMN totp_reset_required INTEGER NOT NULL DEFAULT 0 CHECK (totp_reset_required IN (0, 1));

ALTER TABLE identity_sessions ADD COLUMN purpose TEXT;
ALTER TABLE identity_sessions ADD COLUMN reauthenticated_at TEXT;
ALTER TABLE identity_sessions ADD COLUMN credential_generation INTEGER;
ALTER TABLE browser_sessions ADD COLUMN credential_generation INTEGER;
ALTER TABLE ai_tokens ADD COLUMN credential_generation INTEGER;

ALTER TABLE audit_events ADD COLUMN reason_code TEXT;
ALTER TABLE audit_events ADD COLUMN subject_hash TEXT;

UPDATE audit_events
SET result = 'success'
WHERE result IS NULL;

UPDATE identity_sessions
SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE credential_generation IS NULL;

UPDATE browser_sessions
SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE credential_generation IS NULL;

UPDATE ai_tokens
SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
WHERE credential_generation IS NULL;

CREATE INDEX identity_sessions_account_active_idx
	ON identity_sessions(account_id, revoked_at, expires_at);
CREATE INDEX audit_events_request_id_idx ON audit_events(request_id);
CREATE INDEX audit_events_subject_hash_idx ON audit_events(subject_hash);
