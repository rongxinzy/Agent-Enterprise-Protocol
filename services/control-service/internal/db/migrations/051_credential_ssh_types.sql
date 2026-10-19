-- The credentials table's inline CHECK predates the SSH credential types
-- (ssh_private_key, password) added for asset probing; widen it to match
-- the handler's validation. Caught by real deployment: sqlmock cannot see
-- database-level constraints.
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_type_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_type_check
  CHECK (type IN ('api_key', 'ssh_private_key', 'password'));
