-- Deployment-level runtime settings maintained through the admin API. One row
-- per deployment; each setting is a typed nullable column where NULL means no
-- runtime override and the environment-configured fallback applies.
CREATE TABLE IF NOT EXISTS deployment_settings (
  deployment_id text PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
  model_gateway_base_url text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Existing administrators receive the new permissions through the normal
-- bootstrap permission sync; custom roles must opt in explicitly.
INSERT INTO permissions (id, description) VALUES
  ('deployment.read', 'View deployment runtime settings'),
  ('deployment.write', 'Manage deployment runtime settings')
ON CONFLICT (id) DO NOTHING;
