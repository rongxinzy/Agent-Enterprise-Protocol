CREATE TABLE IF NOT EXISTS models (
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  id text NOT NULL,
  display_name text NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('gateway', 'enterprise_open_source', 'local')),
  protocol text NOT NULL CHECK (protocol = 'openai-compatible'),
  endpoint text,
  upstream_model text,
  local_model_ref text,
  credential_id text,
  capabilities text[] NOT NULL DEFAULT '{}',
  context_window integer CHECK (context_window > 0),
  is_default boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (enterprise_id, id)
);

CREATE TABLE IF NOT EXISTS model_assignments (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL,
  model_id text NOT NULL,
  subject_type text NOT NULL CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent')),
  subject_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (enterprise_id, model_id) REFERENCES models(enterprise_id, id) ON DELETE CASCADE,
  UNIQUE (enterprise_id, model_id, subject_type, subject_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_models_default
  ON models (enterprise_id) WHERE is_default;
CREATE INDEX IF NOT EXISTS idx_model_assignments_subject
  ON model_assignments (enterprise_id, subject_type, subject_id, model_id);
