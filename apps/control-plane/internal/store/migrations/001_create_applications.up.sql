CREATE TABLE IF NOT EXISTS applications (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    source_type TEXT NOT NULL DEFAULT 'image',
    image       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'created',
    replicas    INTEGER NOT NULL DEFAULT 1,
    env_vars    TEXT NOT NULL DEFAULT '{}',
    ports       TEXT NOT NULL DEFAULT '[]',
    volumes     TEXT NOT NULL DEFAULT '[]',
    expose      INTEGER NOT NULL DEFAULT 0,
    domain      TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_applications_name ON applications(name);
