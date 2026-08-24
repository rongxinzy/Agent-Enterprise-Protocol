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
