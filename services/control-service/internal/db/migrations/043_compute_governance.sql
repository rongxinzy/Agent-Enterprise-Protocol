-- Digital-employee compute governance: deployment-level compute settings join
-- the existing runtime settings row, per-agent tier overrides live in their own
-- table, and the delegable compute grant is registered.

ALTER TABLE deployment_settings
  ADD COLUMN IF NOT EXISTS compute_default_tier text,
  ADD COLUMN IF NOT EXISTS compute_idle_hibernate_seconds integer,
  ADD COLUMN IF NOT EXISTS compute_concurrency_cap integer;

-- Per-agent compute tier override. No row means the deployment default tier
-- applies. updated_by records the operator user for the admin audit surface.
CREATE TABLE IF NOT EXISTS agent_compute_assignments (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  agent_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tier text NOT NULL,
  updated_by text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, agent_id)
);

-- Delegable digital-employee compute allocation grant. Existing administrators
-- receive it through the normal bootstrap permission sync; custom roles must
-- opt in explicitly.
INSERT INTO permissions (id, description) VALUES
  ('agents.compute.write', 'Allocate compute tiers to digital employees')
ON CONFLICT (id) DO NOTHING;
