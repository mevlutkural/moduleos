INSERT INTO projects (id, name, slug, network, created_at, updated_at) VALUES
    ('10000000-0000-0000-0000-000000000001', 'Legacy Project', 'legacy-project', 'moduleos-legacy-project', '2025-01-02 03:04:05', '2025-01-02 03:04:05'),
    ('10000000-0000-0000-0000-000000000002', 'Legacy Client', 'legacy-client', 'moduleos-legacy-client', '2025-01-02 03:04:05', '2025-01-02 03:04:05');

INSERT INTO applications (
    id, project_id, name, source_type, image, status, replicas, env_vars, ports,
    volumes, expose, domain, created_at, updated_at
) VALUES
    (
        '20000000-0000-0000-0000-000000000001',
        '10000000-0000-0000-0000-000000000001',
        'legacy-valid', 'image', 'nginx:1.27-alpine', 'running', 2,
        '{"MODE":"production","SECRET":"preserved"}',
        '[{"container_port":80,"published_port":18080,"protocol":"tcp","publish_mode":"ingress"}]',
        '[{"source":"/srv/legacy","target":"/data","read_only":true}]',
        1, 'legacy.example.test', '2025-01-02 03:04:05', '2025-01-03 04:05:06'
    ),
    (
        '20000000-0000-0000-0000-000000000002',
        '00000000-0000-0000-0000-000000000001',
        'legacy-invalid', 'tar', 'legacy-invalid:latest', 'failed', -2,
        'TOKEN=legacy', '18081:81', '/srv/invalid:/data',
        0, '', '2025-02-02 03:04:05', '2025-02-03 04:05:06'
    ),
    (
        '20000000-0000-0000-0000-000000000003',
        '10000000-0000-0000-0000-000000000001',
        'legacy-stopped', 'image', 'redis:7-alpine', 'stopped', 0,
        '{}', '[]', '[]', 0, '', '2025-03-02 03:04:05', '2025-03-03 04:05:06'
    );

INSERT INTO deployments (
    id, app_id, source_type, image, status, triggered_by, error_message, created_at, finished_at
) VALUES
    (
        '30000000-0000-0000-0000-000000000001',
        '20000000-0000-0000-0000-000000000001',
        'image', 'nginx:1.26-alpine', 'success', 'api', '',
        '2025-01-02 03:05:05', '2025-01-02 03:06:05'
    ),
    (
        '30000000-0000-0000-0000-000000000002',
        '20000000-0000-0000-0000-000000000001',
        'image', 'nginx:1.27-alpine', 'failed', 'api', 'legacy failure',
        '2025-01-03 03:05:05', '2025-01-03 03:06:05'
    );

INSERT INTO domains (id, app_id, domain, verified, is_primary, created_at) VALUES
    (
        '40000000-0000-0000-0000-000000000001',
        '20000000-0000-0000-0000-000000000001',
        'custom.legacy.example.test', 1, 1, '2025-01-02 03:07:05'
    );

INSERT INTO project_links (id, source_project_id, target_app_id, alias, created_at) VALUES
    (
        '50000000-0000-0000-0000-000000000001',
        '10000000-0000-0000-0000-000000000002',
        '20000000-0000-0000-0000-000000000001',
        'legacy.service', '2025-01-02 03:08:05'
    );
