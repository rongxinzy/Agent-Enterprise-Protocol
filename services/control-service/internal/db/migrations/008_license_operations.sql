CREATE TABLE IF NOT EXISTS license_audit_events (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id) ON DELETE CASCADE,
  license_id text NOT NULL,
  actor_user_id text NOT NULL,
  action text NOT NULL CHECK (action IN ('import', 'revoke')),
  outcome text NOT NULL CHECK (outcome IN ('success', 'failure')),
  reason text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_license_audit_enterprise_time
  ON license_audit_events (enterprise_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_license_audit_license_time
  ON license_audit_events (license_id, created_at DESC);
