ALTER TABLE upload_sessions ADD COLUMN reserved_temp_bytes INTEGER NOT NULL DEFAULT 0 CHECK (reserved_temp_bytes >= 0);

CREATE INDEX upload_sessions_reservation_admission_idx
	ON upload_sessions(status, account_id, reserved_temp_bytes);

CREATE INDEX upload_sessions_cleanup_reserved_idx
	ON upload_sessions(cleanup_pending, status, lease_expires_at, reserved_temp_bytes);
