UPDATE applications
SET env_vars = '{}'
WHERE json_type(env_vars) <> 'object';

UPDATE applications
SET ports = '[]'
WHERE json_type(ports) <> 'array';

UPDATE applications
SET ports = (
    SELECT COALESCE(
        json_group_array(
            json_object(
                'container_port', COALESCE(json_extract(value, '$.container_port'), 0),
                'published_port', COALESCE(json_extract(value, '$.published_port'), 0),
                'protocol', CASE
                    WHEN COALESCE(json_extract(value, '$.protocol'), '') = '' THEN 'tcp'
                    ELSE json_extract(value, '$.protocol')
                END,
                'publish_mode', CASE
                    WHEN COALESCE(json_extract(value, '$.publish_mode'), '') = '' THEN 'ingress'
                    ELSE json_extract(value, '$.publish_mode')
                END
            )
        ),
        '[]'
    )
    FROM json_each(applications.ports)
)
WHERE json_type(ports) = 'array';

UPDATE applications
SET volumes = '[]'
WHERE json_type(volumes) <> 'array';
