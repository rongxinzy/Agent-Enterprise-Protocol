-- Asset catalog and bindings (FR-AS-01/02). Master data lives in the AEP
-- control plane; assets reference credentials by ID only and never store
-- credential material. Types are validated by the built-in registry in code,
-- so no type table exists in the first phase.
CREATE TABLE IF NOT EXISTS assets (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  id text NOT NULL,
  type_id text NOT NULL,
  name text NOT NULL,
  description text,
  org_team_id text,
  location_id text,
  owner_user_id text,
  undertakers jsonb NOT NULL DEFAULT '[]',
  spec jsonb NOT NULL DEFAULT '{}',
  credential_refs jsonb NOT NULL DEFAULT '{}',
  capabilities_override jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'draft',
  health_state text,
  health_checked_at timestamptz,
  health_detail text,
  source text NOT NULL DEFAULT 'manual',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, id)
);

CREATE INDEX IF NOT EXISTS idx_assets_type ON assets (deployment_id, type_id);
CREATE INDEX IF NOT EXISTS idx_assets_status ON assets (deployment_id, status);

-- Bindings grant usage of an asset (optionally narrowed to specific
-- capabilities) to digital employees, companions, users, or teams. A binding
-- is a usage grant, distinct from the undertaker records kept on the asset.
CREATE TABLE IF NOT EXISTS asset_bindings (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  id text NOT NULL,
  asset_id text NOT NULL,
  subject_type text NOT NULL,
  subject_id text NOT NULL,
  capability_allowlist jsonb,
  granted_by text NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, id),
  FOREIGN KEY (deployment_id, asset_id) REFERENCES assets (deployment_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_bindings_subject
  ON asset_bindings (deployment_id, asset_id, subject_type, subject_id);

-- Existing administrators receive the new permissions through the normal
-- bootstrap permission sync; custom roles must opt in explicitly.
INSERT INTO permissions (id, description) VALUES
  ('assets.read', 'View assets, types, and bindings'),
  ('assets.write', 'Manage assets and bindings')
ON CONFLICT (id) DO NOTHING;
