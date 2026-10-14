CREATE UNIQUE INDEX idx_deployments_id_app_id ON deployments(id, app_id);

CREATE TABLE deployment_order (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id TEXT NOT NULL UNIQUE,
    app_id TEXT NOT NULL,
    FOREIGN KEY (deployment_id, app_id) REFERENCES deployments(id, app_id) ON DELETE CASCADE
);

CREATE INDEX idx_deployment_order_app_sequence ON deployment_order(app_id, sequence DESC);

INSERT INTO deployment_order (deployment_id, app_id)
SELECT id, app_id FROM deployments ORDER BY created_at ASC, id ASC;

CREATE TRIGGER insert_deployment_order AFTER INSERT ON deployments
BEGIN
    INSERT INTO deployment_order (deployment_id, app_id) VALUES (NEW.id, NEW.app_id);
END;

CREATE TABLE deployment_idempotency (
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    key_digest BLOB NOT NULL CHECK(length(key_digest) = 32),
    fingerprint BLOB NOT NULL CHECK(length(fingerprint) = 32),
    deployment_id TEXT NOT NULL,
    accepted_generation INTEGER NOT NULL CHECK(accepted_generation > 0),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK(expires_at > created_at),
    PRIMARY KEY (app_id, key_digest)
);

CREATE INDEX idx_deployment_idempotency_expiry ON deployment_idempotency(app_id, expires_at);
