PRAGMA foreign_keys=OFF;

-- SQLite cannot change foreign_keys while golang-migrate's transaction is
-- active. Preserve all application dependants explicitly so replacing the
-- applications table cannot cascade-delete dependent records.
CREATE TEMP TABLE deployments_v6_backup AS SELECT * FROM deployments;
CREATE TEMP TABLE domains_v6_backup AS SELECT * FROM domains;
CREATE TEMP TABLE project_links_v6_backup AS SELECT * FROM project_links;

ALTER TABLE projects ADD COLUMN observed_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (observed_state IN ('pending', 'ready', 'degraded', 'failed'));
ALTER TABLE projects ADD COLUMN reconcile_error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN reconcile_error_message TEXT NOT NULL DEFAULT '';

CREATE TABLE applications_v0 (
    id                       TEXT PRIMARY KEY,
    project_id               TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    name                     TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 63),
    source_type              TEXT NOT NULL DEFAULT 'image' CHECK (source_type IN ('image', 'tar')),
    image                    TEXT NOT NULL DEFAULT '',
    observed_image           TEXT NOT NULL DEFAULT '',
    status                   TEXT NOT NULL DEFAULT 'created',
    desired_run_state        TEXT NOT NULL DEFAULT 'running'
        CHECK (desired_run_state IN ('running', 'stopped')),
    replicas                 INTEGER NOT NULL DEFAULT 1 CHECK (replicas >= 0),
    resume_replicas          INTEGER NOT NULL DEFAULT 1 CHECK (resume_replicas >= 0),
    desired_generation       INTEGER NOT NULL DEFAULT 1 CHECK (desired_generation >= 0),
    observed_generation      INTEGER NOT NULL DEFAULT 0
        CHECK (observed_generation >= 0 AND observed_generation <= desired_generation),
    observed_state           TEXT NOT NULL DEFAULT 'pending'
        CHECK (observed_state IN ('pending', 'reconciling', 'running', 'stopped', 'degraded', 'failed', 'deleting')),
    reconcile_error_code     TEXT NOT NULL DEFAULT '',
    reconcile_error_message  TEXT NOT NULL DEFAULT '',
    reconcile_retryable      INTEGER NOT NULL DEFAULT 0 CHECK (reconcile_retryable IN (0, 1)),
    reconcile_attempt        INTEGER NOT NULL DEFAULT 0 CHECK (reconcile_attempt >= 0),
    last_transition_at       DATETIME,
    last_reconciled_at       DATETIME,
    env_vars                 TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(env_vars)),
    ports                    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(ports)),
    volumes                  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(volumes)),
    ingress_container_port   INTEGER NOT NULL DEFAULT 0 CHECK (ingress_container_port BETWEEN 0 AND 65535),
    expose                   INTEGER NOT NULL DEFAULT 0 CHECK (expose IN (0, 1)),
    domain                   TEXT NOT NULL DEFAULT '',
    deletion_timestamp       DATETIME,
    finalizer_state          TEXT NOT NULL DEFAULT 'none'
        CHECK (finalizer_state IN ('none', 'pending', 'finalizing')),
    created_at               DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at               DATETIME NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO applications_v0 (
    id, project_id, name, source_type, image, status, desired_run_state,
    replicas, resume_replicas, desired_generation, observed_generation,
    observed_state, env_vars, ports, volumes, expose, domain, created_at, updated_at
)
SELECT
    id,
    CASE
        WHEN project_id IS NOT NULL AND EXISTS (SELECT 1 FROM projects WHERE projects.id = applications.project_id)
            THEN project_id
        ELSE '00000000-0000-0000-0000-000000000001'
    END,
    name,
    CASE WHEN source_type = 'image' THEN source_type ELSE 'image' END,
    image,
    status,
    CASE WHEN status = 'stopped' THEN 'stopped' ELSE 'running' END,
    MAX(replicas, 0),
    CASE WHEN replicas > 0 THEN replicas ELSE 1 END,
    1,
    0,
    CASE WHEN status = 'running' THEN 'running' WHEN status = 'stopped' THEN 'stopped' WHEN status = 'failed' THEN 'failed' ELSE 'pending' END,
    CASE WHEN json_valid(env_vars) THEN env_vars ELSE '{}' END,
    CASE WHEN json_valid(ports) THEN ports ELSE '[]' END,
    CASE WHEN json_valid(volumes) THEN volumes ELSE '[]' END,
    CASE WHEN expose = 1 THEN 1 ELSE 0 END,
    domain,
    created_at,
    updated_at
FROM applications;

DROP TABLE applications;
ALTER TABLE applications_v0 RENAME TO applications;
CREATE UNIQUE INDEX idx_applications_name ON applications(name);
CREATE INDEX idx_applications_project_id ON applications(project_id);
CREATE INDEX idx_applications_reconcile ON applications(deletion_timestamp, desired_generation, observed_generation);

INSERT INTO deployments (
    id, app_id, source_type, image, status, triggered_by, error_message, created_at, finished_at
)
SELECT
    id, app_id, source_type, image, status, triggered_by, error_message, created_at, finished_at
FROM deployments_v6_backup;

INSERT INTO domains (id, app_id, domain, verified, is_primary, created_at)
SELECT id, app_id, domain, verified, is_primary, created_at
FROM domains_v6_backup;

INSERT INTO project_links (id, source_project_id, target_app_id, alias, created_at)
SELECT id, source_project_id, target_app_id, alias, created_at
FROM project_links_v6_backup;

DROP TABLE deployments_v6_backup;
DROP TABLE domains_v6_backup;
DROP TABLE project_links_v6_backup;

ALTER TABLE deployments ADD COLUMN target_generation INTEGER NOT NULL DEFAULT 0 CHECK (target_generation >= 0);
ALTER TABLE deployments ADD COLUMN previous_observed_image TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN started_at DATETIME;
ALTER TABLE deployments ADD COLUMN convergence_deadline DATETIME;
ALTER TABLE deployments ADD COLUMN rollback_source_deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL;

ALTER TABLE project_links ADD COLUMN desired_generation INTEGER NOT NULL DEFAULT 1 CHECK (desired_generation >= 0);
ALTER TABLE project_links ADD COLUMN observed_generation INTEGER NOT NULL DEFAULT 0
    CHECK (observed_generation >= 0 AND observed_generation <= desired_generation);
ALTER TABLE project_links ADD COLUMN deletion_timestamp DATETIME;

CREATE INDEX idx_deployments_app_generation ON deployments(app_id, target_generation DESC, created_at DESC);

PRAGMA foreign_keys=ON;
