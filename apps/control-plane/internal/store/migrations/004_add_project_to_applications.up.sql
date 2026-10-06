ALTER TABLE applications ADD COLUMN project_id TEXT DEFAULT '00000000-0000-0000-0000-000000000001';

CREATE INDEX IF NOT EXISTS idx_applications_project_id ON applications(project_id);
