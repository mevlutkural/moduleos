CREATE TABLE IF NOT EXISTS deployments (
    id            TEXT PRIMARY KEY,
    app_id        TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    source_type   TEXT NOT NULL DEFAULT 'image',
    image         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'pending',
    triggered_by  TEXT NOT NULL DEFAULT 'api',
    error_message TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    finished_at   DATETIME
);

CREATE INDEX IF NOT EXISTS idx_deployments_app_id ON deployments(app_id);
