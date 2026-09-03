CREATE TABLE IF NOT EXISTS enterprises (
  id text PRIMARY KEY,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organizations (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  name text NOT NULL,
  parent_id text REFERENCES organizations(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  username text NOT NULL,
  display_name text NOT NULL,
  email text,
  password_hash text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  require_password_change boolean NOT NULL DEFAULT true,
  is_admin boolean NOT NULL DEFAULT false,
  organization_ids text[] NOT NULL DEFAULT '{}',
  role_ids text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (enterprise_id, username)
);

CREATE TABLE IF NOT EXISTS agents (
  agent_id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  user_id text NOT NULL REFERENCES users(id),
  agent_version text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('windows', 'macos', 'linux')),
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  applied_skill_revision text,
  installed_skill_ids text[] NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS refresh_sessions (
  token_hash text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  user_id text NOT NULL REFERENCES users(id),
  agent_id text NOT NULL REFERENCES agents(agent_id),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS skills (
  id text PRIMARY KEY,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS skill_versions (
  skill_id text NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  version text NOT NULL,
  object_key text NOT NULL,
  sha256 text NOT NULL,
  size_bytes bigint NOT NULL,
  published boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  PRIMARY KEY (skill_id, version)
);

CREATE TABLE IF NOT EXISTS skill_assignments (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  skill_id text NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  subject_type text NOT NULL CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent')),
  subject_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (enterprise_id, skill_id, subject_type, subject_id)
);

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
  reasoning_compatibility jsonb CHECK (reasoning_compatibility IS NULL OR jsonb_typeof(reasoning_compatibility) = 'object'),
  context_window integer CHECK (context_window > 0),
  is_default boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (enterprise_id, id),
  FOREIGN KEY (enterprise_id, credential_id) REFERENCES credentials(enterprise_id, id) ON DELETE RESTRICT
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

CREATE TABLE IF NOT EXISTS control_events (
  event_id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  type text NOT NULL,
  scope_type text NOT NULL CHECK (scope_type IN ('global', 'organization', 'user', 'agent')),
  scope_id text,
  resource_type text,
  resource_id text,
  resource_revision text,
  task_type text NOT NULL,
  supersedes_key text,
  state text NOT NULL DEFAULT 'active',
  expires_at timestamptz NOT NULL,
  created_by text NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control_deliveries (
  cursor bigserial UNIQUE,
  delivery_id text PRIMARY KEY,
  event_id text NOT NULL REFERENCES control_events(event_id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES agents(agent_id),
  state text NOT NULL DEFAULT 'pending',
  attempt_count integer NOT NULL DEFAULT 0,
  received_at timestamptz,
  started_at timestamptz,
  completed_at timestamptz,
  applied_revision text,
  error_code text,
  message text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_id, agent_id)
);

CREATE TABLE IF NOT EXISTS telemetry_events (
  event_id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  user_id text NOT NULL REFERENCES users(id),
  agent_id text NOT NULL REFERENCES agents(agent_id),
  type text NOT NULL,
  resource_type text,
  resource_id text,
  result text,
  payload jsonb NOT NULL DEFAULT '{}',
  occurred_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS skill_sync_results (
  id text PRIMARY KEY,
  enterprise_id text NOT NULL REFERENCES enterprises(id),
  user_id text NOT NULL REFERENCES users(id),
  agent_id text NOT NULL REFERENCES agents(agent_id),
  revision text NOT NULL,
  status text NOT NULL,
  installed_skill_ids text[] NOT NULL DEFAULT '{}',
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS data_plane_desired_states (
  enterprise_id text PRIMARY KEY REFERENCES enterprises(id) ON DELETE CASCADE,
  revision text NOT NULL,
  routes jsonb NOT NULL,
  content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
  published_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS data_plane_statuses (
  enterprise_id text PRIMARY KEY REFERENCES enterprises(id) ON DELETE CASCADE,
  state text NOT NULL CHECK (state IN ('pending', 'applying', 'ready', 'degraded', 'error')),
  observed_revision text,
  content_hash text CHECK (content_hash IS NULL OR content_hash ~ '^[a-f0-9]{64}$'),
  last_applied_at timestamptz,
  error_code text,
  message text,
  resource_count integer NOT NULL DEFAULT 0 CHECK (resource_count >= 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agents_user ON agents (enterprise_id, user_id);
CREATE INDEX IF NOT EXISTS idx_deliveries_agent_state ON control_deliveries (agent_id, state, cursor);
CREATE INDEX IF NOT EXISTS idx_telemetry_search ON telemetry_events (enterprise_id, agent_id, occurred_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_models_default ON models (enterprise_id) WHERE is_default;
CREATE INDEX IF NOT EXISTS idx_model_assignments_subject ON model_assignments (enterprise_id, subject_type, subject_id, model_id);
CREATE INDEX IF NOT EXISTS idx_credential_assignments_subject ON credential_assignments (enterprise_id, subject_type, subject_id, credential_id);
CREATE INDEX IF NOT EXISTS idx_credential_resolution_audit ON credential_resolution_audit (enterprise_id, credential_id, created_at DESC);

CREATE TABLE IF NOT EXISTS login_rate_limits (
  key_hash text PRIMARY KEY,
  failure_count integer NOT NULL CHECK (failure_count > 0),
  blocked_until timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);

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

CREATE INDEX IF NOT EXISTS idx_login_rate_limits_updated ON login_rate_limits (updated_at);
CREATE INDEX IF NOT EXISTS idx_authentication_audit_enterprise_time ON authentication_audit_events (enterprise_id, created_at DESC);

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

CREATE INDEX IF NOT EXISTS idx_licenses_enterprise_status ON licenses (enterprise_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_license_activations_enterprise ON license_activations (enterprise_id, license_id, revoked_at);
