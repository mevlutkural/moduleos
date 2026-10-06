ALTER TABLE projects ADD COLUMN deletion_timestamp DATETIME;
ALTER TABLE projects ADD COLUMN finalizer_state TEXT NOT NULL DEFAULT 'none'
    CHECK (finalizer_state IN ('none', 'pending'));

CREATE INDEX idx_projects_deletion_timestamp ON projects(deletion_timestamp);
