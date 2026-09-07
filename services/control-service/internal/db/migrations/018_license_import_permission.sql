-- License import mutates the deployment catalog and must not be granted by a
-- read-only License operator.
INSERT INTO permissions (id, description)
VALUES ('licenses.write', 'Import and replace deployment Licenses')
ON CONFLICT (id) DO NOTHING;
