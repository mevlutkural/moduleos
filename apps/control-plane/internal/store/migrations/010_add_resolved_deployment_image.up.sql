ALTER TABLE deployments ADD COLUMN resolved_image TEXT NOT NULL DEFAULT '';

UPDATE deployments
SET resolved_image = image
WHERE status IN ('success', 'succeeded')
  AND instr(image, '@sha256:') > 1
  AND length(image) = instr(image, '@sha256:') + 71
  AND substr(image, instr(image, '@sha256:') + 8) NOT GLOB '*[^0-9A-Fa-f]*';
