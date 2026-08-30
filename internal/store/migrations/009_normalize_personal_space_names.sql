-- Personal spaces are identified by kind and owner, not by an email-derived
-- display name. Store a neutral name; clients localize it for the active UI.
UPDATE spaces
SET name = 'My Space',
	updated_at = CURRENT_TIMESTAMP
WHERE kind = 'personal'
  AND name <> 'My Space';
