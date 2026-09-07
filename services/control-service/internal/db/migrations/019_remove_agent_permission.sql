-- The Admin Console no longer exposes an Agent resource. Agent terminals are
-- represented by user_sessions, so agents.read is a dead permission and must
-- not remain grantable in the deployment RBAC catalog.
DELETE FROM role_permissions
WHERE permission_id = 'agents.read';

DELETE FROM permissions
WHERE id = 'agents.read';
