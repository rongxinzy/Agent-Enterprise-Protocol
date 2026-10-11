-- Server-side audit of administrative write operations. Before this table the
-- control service recorded only authentication, credential resolution and
-- license events, so "who changed this user/model/skill" could not be answered.
CREATE TABLE IF NOT EXISTS admin_audit_events (
  cursor bigserial PRIMARY KEY,
  deployment_id text NOT NULL,
  actor_user_id text,
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id text,
  result text NOT NULL CHECK (result IN ('success', 'failure')),
  reason text,
  payload jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_audit_deployment_cursor
  ON admin_audit_events (deployment_id, cursor);
CREATE INDEX IF NOT EXISTS idx_admin_audit_deployment_time
  ON admin_audit_events (deployment_id, created_at DESC);
