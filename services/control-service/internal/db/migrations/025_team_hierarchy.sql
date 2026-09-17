-- Team hierarchy: Teams double as the department tree for the digital
-- employee system. Materialized path + depth keep subtree expansion
-- (management scope) a single indexed lookup without a closure table.
ALTER TABLE teams ADD COLUMN parent_team_id text;
ALTER TABLE teams ADD COLUMN path text NOT NULL DEFAULT '/';
ALTER TABLE teams ADD COLUMN depth integer NOT NULL DEFAULT 0;
ALTER TABLE teams ADD CONSTRAINT teams_depth_check CHECK (depth >= 0);
ALTER TABLE teams ADD CONSTRAINT teams_parent_fk
  FOREIGN KEY (deployment_id, parent_team_id)
  REFERENCES teams (deployment_id, id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_teams_parent
  ON teams (deployment_id, parent_team_id);
CREATE INDEX IF NOT EXISTS idx_teams_path
  ON teams (deployment_id, path text_pattern_ops);
