-- Data scope rules: management scope, single-user exception grants, and
-- explicit denies. Evaluation precedence is
-- explicit_deny > exception_grant > management_scope > the implicit
-- own-department baseline (the user's own team subtree, which needs no rule
-- row). `department_default` is reserved for a future migration and is not
-- accepted by the API yet. `resource_kind` is a deployment-defined opaque
-- identifier; `team` is the one protocol-defined kind (it expands the
-- organizational scope instead of listing a resource).
CREATE TABLE IF NOT EXISTS data_scope_rules (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  id text NOT NULL,
  rule_kind text NOT NULL CHECK (rule_kind IN
    ('management_scope', 'exception_grant', 'explicit_deny')),
  subject_type text NOT NULL CHECK (subject_type IN ('user', 'role', 'team')),
  subject_id text NOT NULL,
  resource_kind text NOT NULL CHECK (resource_kind ~ '^[a-z][a-z0-9_-]{0,63}$'),
  resource_id text NOT NULL,
  starts_at timestamptz,
  expires_at timestamptz,
  reason text NOT NULL DEFAULT '',
  created_by text NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, id),
  UNIQUE (deployment_id, rule_kind, subject_type, subject_id, resource_kind, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_data_scope_rules_subject
  ON data_scope_rules (deployment_id, subject_type, subject_id, resource_kind);
CREATE INDEX IF NOT EXISTS idx_data_scope_rules_resource
  ON data_scope_rules (deployment_id, resource_kind, resource_id);
CREATE INDEX IF NOT EXISTS idx_data_scope_rules_expiry
  ON data_scope_rules (expires_at, id) WHERE expires_at IS NOT NULL;

INSERT INTO permissions (id, description) VALUES
  ('data_scope.read', 'View data scope rules and retrieval contexts'),
  ('data_scope.write', 'Manage data scope rules'),
  ('identity.read', 'View identity sources and mappings'),
  ('identity.write', 'Manage identity sources and mappings')
ON CONFLICT (id) DO NOTHING;
