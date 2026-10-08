-- A live mount root is a single attachment point. This database constraint
-- closes the race between validation and insertion when two administrators
-- concurrently register the same external directory.
CREATE UNIQUE INDEX mounts_active_root_path_unique
ON mounts(root_path)
WHERE status <> 'deleted';
