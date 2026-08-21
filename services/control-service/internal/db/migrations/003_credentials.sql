CREATE TABLE IF NOT EXISTS credentials (
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  id text NOT NULL,
  name text NOT NULL,
  service text NOT NULL,
  type text NOT NULL CHECK (type = 'api_key'),
  delivery_mode text NOT NULL CHECK (delivery_mode IN ('server_only', 'agent')),
  encrypted_value bytea NOT NULL,
  nonce bytea NOT NULL,
  key_id text NOT NULL,
  masked_value text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  rotated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (enterprise_id, id)
);

CREATE TABLE IF NOT EXISTS credential_assignments (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL,
  credential_id text NOT NULL,
  subject_type text NOT NULL CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent')),
  subject_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (enterprise_id, credential_id) REFERENCES credentials(enterprise_id, id) ON DELETE CASCADE,
  UNIQUE (enterprise_id, credential_id, subject_type, subject_id)
);

CREATE TABLE IF NOT EXISTS credential_resolution_audit (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  credential_id text NOT NULL,
  user_id text NOT NULL REFERENCES users(id),
  agent_id text NOT NULL REFERENCES agents(agent_id),
  purpose text NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('resolved', 'denied')),
  reason text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_credential_assignments_subject
  ON credential_assignments (enterprise_id, subject_type, subject_id, credential_id);
CREATE INDEX IF NOT EXISTS idx_credential_resolution_audit
  ON credential_resolution_audit (enterprise_id, credential_id, created_at DESC);

ALTER TABLE models
  ADD CONSTRAINT models_credential_fk
  FOREIGN KEY (enterprise_id, credential_id)
  REFERENCES credentials(enterprise_id, id)
  ON DELETE RESTRICT
  NOT VALID;
