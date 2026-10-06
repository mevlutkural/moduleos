CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    network     TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug);

-- root project seed
INSERT OR IGNORE INTO projects (id, name, slug, network, created_at, updated_at)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'root',
    'root',
    'moduleos-root-net',
    datetime('now'),
    datetime('now')
);
