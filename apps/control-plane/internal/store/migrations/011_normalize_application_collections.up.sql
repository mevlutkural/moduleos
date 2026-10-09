UPDATE applications
SET ports = '[]'
WHERE json_type(ports) <> 'array';

UPDATE applications
SET volumes = '[]'
WHERE json_type(volumes) <> 'array';
