DROP INDEX IF EXISTS idx_projects_deletion_timestamp;
ALTER TABLE projects DROP COLUMN finalizer_state;
ALTER TABLE projects DROP COLUMN deletion_timestamp;
