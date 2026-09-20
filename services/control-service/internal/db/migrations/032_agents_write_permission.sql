-- Delegable digital-employee lifecycle authority: creating and managing
-- digital employees no longer requires the human-account users.write grant.
-- Existing administrators keep working through the OR gate in the route
-- permission check.
INSERT INTO permissions (id, description) VALUES
  ('agents.write', 'Create, update, and delete digital employees')
ON CONFLICT (id) DO NOTHING;
