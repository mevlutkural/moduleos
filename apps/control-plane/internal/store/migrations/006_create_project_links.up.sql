CREATE TABLE project_links (
    id                TEXT PRIMARY KEY,
    source_project_id TEXT NOT NULL,
    target_app_id     TEXT NOT NULL,
    alias             TEXT NOT NULL,
    created_at        DATETIME NOT NULL,

    FOREIGN KEY (source_project_id)
        REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (target_app_id)
        REFERENCES applications(id) ON DELETE CASCADE,

    UNIQUE (source_project_id, target_app_id),
    UNIQUE (source_project_id, alias)
);

CREATE INDEX idx_project_links_source_project
    ON project_links(source_project_id);

CREATE INDEX idx_project_links_target_app
    ON project_links(target_app_id);
