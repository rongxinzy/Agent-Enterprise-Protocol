-- Credential resolution is a user-session operation. Add the session
-- reference for databases upgraded from the pre-session audit schema.
ALTER TABLE credential_resolution_audit
  ADD COLUMN IF NOT EXISTS session_id text REFERENCES user_sessions(session_id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_credential_resolution_audit_session
  ON credential_resolution_audit (deployment_id, session_id, created_at DESC);
