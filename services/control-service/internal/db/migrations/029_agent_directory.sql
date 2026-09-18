-- Digital employee directory: agents are users (service accounts) with
-- kind='agent' plus a profile row. Presence reuses user_sessions heartbeats;
-- skills and model authorization reuse the existing assignment union.
ALTER TABLE users ADD COLUMN kind text NOT NULL DEFAULT 'human';
ALTER TABLE users ADD CONSTRAINT users_kind_check CHECK (kind IN ('human', 'agent'));

CREATE TABLE IF NOT EXISTS agent_profiles (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  display_title text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  avatar_object_key text,
  home_team_id text NOT NULL,
  prompt_skill_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, user_id),
  CONSTRAINT agent_profiles_home_team_fk
    FOREIGN KEY (deployment_id, home_team_id)
    REFERENCES teams (deployment_id, id) ON DELETE RESTRICT,
  CONSTRAINT agent_profiles_prompt_skill_fk
    FOREIGN KEY (prompt_skill_id) REFERENCES skills(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_users_kind_agent
  ON users (deployment_id, kind) WHERE kind = 'agent';
CREATE INDEX IF NOT EXISTS idx_agent_profiles_home_team
  ON agent_profiles (deployment_id, home_team_id);
