CREATE TABLE IF NOT EXISTS domains (
    id         TEXT PRIMARY KEY,
    app_id     TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    domain     TEXT NOT NULL UNIQUE,
    verified   INTEGER NOT NULL DEFAULT 0,
    is_primary INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_domains_app_id ON domains(app_id);
