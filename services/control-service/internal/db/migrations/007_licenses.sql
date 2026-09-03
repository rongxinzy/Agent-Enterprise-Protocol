CREATE TABLE IF NOT EXISTS licenses (
  license_id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id) ON DELETE CASCADE,
  customer_id text NOT NULL,
  deployment_id text NOT NULL,
  digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
  key_id text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  issued_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  grace_ends_at timestamptz NOT NULL,
  user_limit integer NOT NULL CHECK (user_limit > 0),
  agent_limit integer NOT NULL CHECK (agent_limit > 0),
  features text[] NOT NULL DEFAULT '{}',
  payload jsonb NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS license_activations (
  id text PRIMARY KEY,
  license_id text NOT NULL REFERENCES licenses(license_id) ON DELETE CASCADE,
  enterprise_id text NOT NULL REFERENCES enterprises(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
  activated_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  UNIQUE (license_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_licenses_enterprise_status
  ON licenses (enterprise_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_license_activations_enterprise
  ON license_activations (enterprise_id, license_id, revoked_at);
