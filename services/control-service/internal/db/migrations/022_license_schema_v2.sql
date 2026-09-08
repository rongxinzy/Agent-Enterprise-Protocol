-- License v2 is not a seat or device-count license. User/team membership and
-- client sessions are deployment data, not signed License quotas.
ALTER TABLE licenses
  ALTER COLUMN expires_at DROP NOT NULL,
  ALTER COLUMN grace_ends_at DROP NOT NULL;

ALTER TABLE licenses DROP COLUMN IF EXISTS user_limit;
ALTER TABLE licenses DROP COLUMN IF EXISTS activation_limit;
ALTER TABLE licenses DROP COLUMN IF EXISTS agent_limit;

ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_user_limit_check;
ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_activation_limit_check;
ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_agent_limit_check;
