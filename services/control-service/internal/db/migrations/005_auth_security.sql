CREATE TABLE IF NOT EXISTS login_rate_limits (
  key_hash text PRIMARY KEY,
  failure_count integer NOT NULL CHECK (failure_count > 0),
  blocked_until timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_login_rate_limits_updated
  ON login_rate_limits (updated_at);

CREATE TABLE IF NOT EXISTS authentication_audit_events (
  cursor bigserial PRIMARY KEY,
  enterprise_id text NOT NULL,
  user_id text,
  agent_id text NOT NULL,
  event_type text NOT NULL CHECK (event_type IN ('login.succeeded', 'login.failed', 'login.throttled', 'password.changed')),
  outcome text NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
  reason text,
  principal_hash text NOT NULL,
  source_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_authentication_audit_enterprise_time
  ON authentication_audit_events (enterprise_id, created_at DESC);
