-- SQLite ALTER TABLE DROP COLUMN requires 3.35+
-- Safe approach: recreate the table
CREATE TABLE applications_backup AS SELECT id, name, source_type, image, status, replicas, env_vars, ports, volumes, expose, domain, created_at, updated_at FROM applications;
DROP TABLE applications;
ALTER TABLE applications_backup RENAME TO applications;
