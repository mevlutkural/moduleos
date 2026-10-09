UPDATE applications
SET env_vars = '{}'
WHERE json_type(env_vars) <> 'object';

UPDATE applications
SET ports = '[]'
WHERE json_type(ports) <> 'array';

UPDATE applications
SET ports = '[]'
WHERE json_type(ports) = 'array'
  AND EXISTS (
      SELECT 1
      FROM json_each(applications.ports)
      WHERE type <> 'object'
  );

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
    FROM (
        SELECT value
        FROM json_each(applications.ports)
        ORDER BY
            COALESCE(json_extract(value, '$.container_port'), 0),
            COALESCE(json_extract(value, '$.published_port'), 0),
            CASE
                WHEN COALESCE(json_extract(value, '$.protocol'), '') = '' THEN 'tcp'
                ELSE json_extract(value, '$.protocol')
            END,
            CASE
                WHEN COALESCE(json_extract(value, '$.publish_mode'), '') = '' THEN 'ingress'
                ELSE json_extract(value, '$.publish_mode')
            END
    )
)
WHERE json_type(ports) = 'array';

UPDATE applications
SET volumes = '[]'
WHERE json_type(volumes) <> 'array';

UPDATE applications
SET volumes = '[]'
WHERE json_type(volumes) = 'array'
  AND EXISTS (
      SELECT 1
      FROM json_each(applications.volumes)
      WHERE type <> 'object'
         OR json_type(value, '$.source') IS NOT 'text'
         OR json_type(value, '$.target') IS NOT 'text'
         OR COALESCE(json_type(value, '$.read_only'), '') NOT IN ('true', 'false')
         OR substr(json_extract(value, '$.source'), 1, 1) <> '/'
         OR substr(json_extract(value, '$.target'), 1, 1) <> '/'
         OR instr(json_extract(value, '$.source'), char(0)) > 0
         OR instr(json_extract(value, '$.target'), char(0)) > 0
  );

UPDATE applications
SET volumes = (
    WITH RECURSIVE
    entries AS (
        SELECT
            CAST(key AS INTEGER) AS position,
            json_extract(value, '$.source') AS source,
            json_extract(value, '$.target') AS target,
            json_extract(value, '$.read_only') AS read_only
        FROM json_each(applications.volumes)
    ),
    paths(position, kind, remaining, segments) AS (
        SELECT position, 'source', ltrim(source, '/'), json('[]')
        FROM entries
        UNION ALL
        SELECT position, 'target', ltrim(target, '/'), json('[]')
        FROM entries
        UNION ALL
        SELECT
            position,
            kind,
            CASE
                WHEN instr(remaining, '/') = 0 THEN ''
                ELSE substr(remaining, instr(remaining, '/') + 1)
            END,
            CASE
                WHEN CASE
                    WHEN instr(remaining, '/') = 0 THEN remaining
                    ELSE substr(remaining, 1, instr(remaining, '/') - 1)
                END IN ('', '.') THEN segments
                WHEN CASE
                    WHEN instr(remaining, '/') = 0 THEN remaining
                    ELSE substr(remaining, 1, instr(remaining, '/') - 1)
                END = '..' THEN CASE
                    WHEN json_array_length(segments) = 0 THEN segments
                    ELSE json_remove(segments, '$[#-1]')
                END
                ELSE json_insert(
                    segments,
                    '$[#]',
                    CASE
                        WHEN instr(remaining, '/') = 0 THEN remaining
                        ELSE substr(remaining, 1, instr(remaining, '/') - 1)
                    END
                )
            END
        FROM paths
        WHERE remaining <> ''
    ),
    cleaned AS (
        SELECT
            position,
            kind,
            '/' || COALESCE((
                SELECT group_concat(value, '/')
                FROM json_each(paths.segments)
            ), '') AS path
        FROM paths
        WHERE remaining = ''
    ),
    normalized AS (
        SELECT
            entries.position,
            source.path AS source,
            target.path AS target,
            entries.read_only
        FROM entries
        JOIN cleaned AS source
          ON source.position = entries.position AND source.kind = 'source'
        JOIN cleaned AS target
          ON target.position = entries.position AND target.kind = 'target'
    )
    SELECT COALESCE(
        json_group_array(
            json_object(
                'source', source,
                'target', target,
                'read_only', json(CASE WHEN read_only THEN 'true' ELSE 'false' END)
            )
        ),
        '[]'
    )
    FROM (
        SELECT source, target, read_only
        FROM normalized
        ORDER BY target, source, position
    )
)
WHERE json_type(volumes) = 'array';

UPDATE applications
SET volumes = '[]'
WHERE json_type(volumes) = 'array'
  AND (
      EXISTS (
          SELECT 1
          FROM json_each(applications.volumes)
          WHERE json_extract(value, '$.target') = '/'
      )
      OR EXISTS (
          SELECT 1
          FROM json_each(applications.volumes)
          GROUP BY json_extract(value, '$.target')
          HAVING COUNT(*) > 1
      )
  );
