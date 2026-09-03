-- Transitional RBAC foundation. Existing enterprise/organization/agent columns
-- remain until the runtime query cutover migration is applied.
CREATE TABLE IF NOT EXISTS deployments (
  id text PRIMARY KEY,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO deployments (id, name)
SELECT id, name FROM enterprises
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS permissions (
  id text PRIMARY KEY,
  description text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS roles (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  id text NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  built_in boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, id),
  UNIQUE (deployment_id, name)
);

CREATE TABLE IF NOT EXISTS role_permissions (
  deployment_id text NOT NULL,
  role_id text NOT NULL,
  permission_id text NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
  PRIMARY KEY (deployment_id, role_id, permission_id),
  FOREIGN KEY (deployment_id, role_id) REFERENCES roles(deployment_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS teams (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  id text NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  built_in boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, id),
  UNIQUE (deployment_id, name)
);

CREATE TABLE IF NOT EXISTS user_role_bindings (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id text NOT NULL,
  is_primary boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, user_id, role_id),
  FOREIGN KEY (deployment_id, role_id) REFERENCES roles(deployment_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS user_team_bindings (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  team_id text NOT NULL,
  is_primary boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, user_id, team_id),
  FOREIGN KEY (deployment_id, team_id) REFERENCES teams(deployment_id, id) ON DELETE CASCADE
);

INSERT INTO permissions (id, description) VALUES
  ('users.read', 'View users'), ('users.write', 'Create and update users'),
  ('roles.read', 'View roles and permissions'), ('roles.write', 'Manage roles and permissions'),
  ('teams.read', 'View teams'), ('teams.write', 'Create and update teams and membership'),
  ('agents.read', 'View client instances'),
  ('models.read', 'View enterprise models'), ('models.write', 'Create and update models'),
  ('models.assign', 'Assign models to users, roles, or teams'),
  ('skills.read', 'View Skills'), ('skills.write', 'Create and publish Skills'),
  ('skills.assign', 'Assign Skills to users, roles, or teams'),
  ('credentials.read', 'View credential metadata'), ('credentials.write', 'Manage credentials'),
  ('credentials.assign', 'Assign credentials to users, roles, or teams'),
  ('events.read', 'View control events'), ('events.write', 'Create and cancel control events'),
  ('licenses.read', 'View License status'), ('licenses.revoke', 'Revoke a License'),
  ('audit.read', 'View audit records'), ('data_plane.write', 'Manage data-plane state')
ON CONFLICT (id) DO NOTHING;

-- Preserve existing role IDs and make the former admin role explicit.
INSERT INTO roles (deployment_id, id, name, built_in)
SELECT u.enterprise_id, role_id, role_id, role_id = 'admin'
FROM users u CROSS JOIN LATERAL unnest(u.role_ids) AS role_id
WHERE role_id <> ''
ON CONFLICT (deployment_id, id) DO NOTHING;
INSERT INTO roles (deployment_id, id, name, built_in)
SELECT id, 'admin', 'Administrator', true FROM deployments
ON CONFLICT (deployment_id, id) DO NOTHING;

INSERT INTO role_permissions (deployment_id, role_id, permission_id)
SELECT r.deployment_id, r.id, p.id
FROM roles r CROSS JOIN permissions p
WHERE r.id = 'admin'
ON CONFLICT DO NOTHING;

INSERT INTO teams (deployment_id, id, name, description, built_in)
SELECT id, 'all-users', 'All users', 'Default team for every deployment user', true
FROM deployments
ON CONFLICT (deployment_id, id) DO NOTHING;

INSERT INTO user_role_bindings (deployment_id, user_id, role_id, is_primary)
SELECT u.enterprise_id, u.id, role_id, row_number() OVER (PARTITION BY u.id ORDER BY role_id) = 1
FROM users u CROSS JOIN LATERAL unnest(u.role_ids) AS role_id
WHERE role_id <> ''
ON CONFLICT DO NOTHING;
INSERT INTO user_role_bindings (deployment_id, user_id, role_id, is_primary)
SELECT u.enterprise_id, u.id, 'admin', true FROM users u WHERE u.is_admin
ON CONFLICT DO NOTHING;

INSERT INTO user_team_bindings (deployment_id, user_id, team_id, is_primary)
SELECT u.enterprise_id, u.id, 'all-users', true FROM users u
ON CONFLICT DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_roles_deployment_enabled ON roles (deployment_id, enabled, id);
CREATE INDEX IF NOT EXISTS idx_role_permissions_permission ON role_permissions (deployment_id, permission_id, role_id);
CREATE INDEX IF NOT EXISTS idx_teams_deployment_enabled ON teams (deployment_id, enabled, id);
CREATE INDEX IF NOT EXISTS idx_user_roles_user ON user_role_bindings (deployment_id, user_id, role_id);
CREATE INDEX IF NOT EXISTS idx_user_teams_user ON user_team_bindings (deployment_id, user_id, team_id);

ALTER TABLE skill_assignments DROP CONSTRAINT IF EXISTS skill_assignments_subject_type_check;
ALTER TABLE skill_assignments ADD CONSTRAINT skill_assignments_subject_type_check CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent', 'role', 'team'));
ALTER TABLE model_assignments DROP CONSTRAINT IF EXISTS model_assignments_subject_type_check;
ALTER TABLE model_assignments ADD CONSTRAINT model_assignments_subject_type_check CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent', 'role', 'team'));
ALTER TABLE credential_assignments DROP CONSTRAINT IF EXISTS credential_assignments_subject_type_check;
ALTER TABLE credential_assignments ADD CONSTRAINT credential_assignments_subject_type_check CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent', 'role', 'team'));
