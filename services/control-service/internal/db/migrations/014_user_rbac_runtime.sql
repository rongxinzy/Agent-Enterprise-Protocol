-- Canonical runtime cutover: a terminal is represented by a user session.
-- Legacy Agent tables/columns remain readable for one migration window and
-- are removed by the follow-up identity cleanup migration.

ALTER TABLE authentication_audit_events
  ADD COLUMN IF NOT EXISTS session_id text REFERENCES user_sessions(session_id) ON DELETE SET NULL;
ALTER TABLE authentication_audit_events
  ALTER COLUMN agent_id DROP NOT NULL;

ALTER TABLE skill_sync_results
  ADD COLUMN IF NOT EXISTS session_id text REFERENCES user_sessions(session_id) ON DELETE SET NULL;
ALTER TABLE skill_sync_results
  ALTER COLUMN agent_id DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_skill_sync_results_session
  ON skill_sync_results (deployment_id, session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_authentication_audit_session_time
  ON authentication_audit_events (deployment_id, session_id, created_at DESC);

-- Existing grants are normalized to the RBAC subjects by 010. The runtime
-- resolver only emits user/role/team grants; the old subject constraint is
-- intentionally retained until PR #54 so older installations can upgrade.
