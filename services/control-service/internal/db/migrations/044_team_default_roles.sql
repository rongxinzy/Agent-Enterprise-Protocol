CREATE TABLE team_default_roles (
  deployment_id text NOT NULL,
  team_id text NOT NULL,
  role_id text NOT NULL,
  PRIMARY KEY (deployment_id, team_id, role_id),
  FOREIGN KEY (deployment_id, team_id) REFERENCES teams(deployment_id, id) ON DELETE CASCADE,
  FOREIGN KEY (deployment_id, role_id) REFERENCES roles(deployment_id, id) ON DELETE RESTRICT
);
CREATE INDEX team_default_roles_role_idx ON team_default_roles(deployment_id, role_id);

-- Keep inherited roles separate from explicit grants: leaving/disabling a
-- team or changing defaults must never delete a user's explicit binding.
CREATE VIEW user_effective_roles AS
SELECT b.deployment_id, b.user_id, b.role_id
FROM user_role_bindings b
JOIN roles r ON r.deployment_id=b.deployment_id AND r.id=b.role_id AND r.enabled
UNION
SELECT b.deployment_id, b.user_id, d.role_id
FROM user_team_bindings b
JOIN teams t ON t.deployment_id=b.deployment_id AND t.id=b.team_id AND t.enabled
JOIN team_default_roles d ON d.deployment_id=b.deployment_id AND d.team_id=b.team_id
JOIN roles r ON r.deployment_id=d.deployment_id AND r.id=d.role_id AND r.enabled;
