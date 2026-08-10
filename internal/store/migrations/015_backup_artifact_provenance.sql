ALTER TABLE backups ADD COLUMN sha256 TEXT;
ALTER TABLE backups ADD COLUMN size_bytes INTEGER CHECK (size_bytes IS NULL OR size_bytes >= 0);
ALTER TABLE backups ADD COLUMN canonical_path TEXT;
ALTER TABLE backups ADD COLUMN schema_version INTEGER CHECK (schema_version IS NULL OR schema_version >= 0);
