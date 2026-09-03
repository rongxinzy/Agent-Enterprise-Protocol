-- User sessions are terminal/session identities, not long-lived Agent records.
-- The legacy refresh_sessions table remains untouched until the final Agent
-- removal migration; new logins use these tables exclusively.
CREATE TABLE IF NOT EXISTS user_sessions (
  session_id text PRIMARY KEY,
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  topic text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz
);

CREATE TABLE IF NOT EXISTS user_session_tokens (
  token_hash text PRIMARY KEY,
  session_id text NOT NULL REFERENCES user_sessions(session_id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_active
  ON user_sessions (deployment_id, user_id, revoked_at, last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_session_tokens_session
  ON user_session_tokens (session_id, revoked_at, expires_at);
