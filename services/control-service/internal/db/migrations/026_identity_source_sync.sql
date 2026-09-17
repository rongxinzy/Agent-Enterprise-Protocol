-- Identity sources and mappings: landing ground for the reserved federated
-- identity contract and HR/AD/LDAP/Feishu/WeCom directory synchronization.
-- Source config is plaintext metadata only; every secret stays in the
-- credential store.
CREATE TABLE IF NOT EXISTS identity_sources (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('ldap', 'ad', 'oidc', 'hr', 'feishu', 'wecom')),
  display_name text NOT NULL,
  config jsonb NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, id)
);

CREATE TABLE IF NOT EXISTS identity_mappings (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  source_id text NOT NULL,
  external_subject_type text NOT NULL CHECK (external_subject_type IN ('user', 'team')),
  external_id text NOT NULL,
  local_subject_id text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  linked_at timestamptz NOT NULL DEFAULT now(),
  last_synced_at timestamptz,
  PRIMARY KEY (deployment_id, source_id, external_subject_type, external_id),
  FOREIGN KEY (deployment_id, source_id)
    REFERENCES identity_sources (deployment_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_identity_mappings_local
  ON identity_mappings (deployment_id, external_subject_type, local_subject_id);
