-- Normalize built-in RBAC labels for deployments upgraded from the legacy
-- enterprise/organization model. The IDs are reserved by the bootstrap
-- contract, so updating them is safe and keeps the Admin Console stable.
UPDATE roles
SET name = 'Administrator',
    built_in = true,
    enabled = true,
    updated_at = now()
WHERE id = 'admin';

UPDATE teams
SET name = 'All users',
    description = 'Default team for every deployment user',
    built_in = true,
    enabled = true,
    updated_at = now()
WHERE id = 'all-users';
