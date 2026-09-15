CREATE INDEX IF NOT EXISTS idx_authentication_audit_retention
  ON authentication_audit_events (created_at, cursor);
CREATE INDEX IF NOT EXISTS idx_credential_resolution_audit_retention
  ON credential_resolution_audit (created_at, id);
CREATE INDEX IF NOT EXISTS idx_license_audit_retention
  ON license_audit_events (created_at, id);
CREATE INDEX IF NOT EXISTS idx_telemetry_retention
  ON telemetry_events (received_at, event_id);
CREATE INDEX IF NOT EXISTS idx_skill_sync_retention
  ON skill_sync_results (created_at, id);
CREATE INDEX IF NOT EXISTS idx_session_deliveries_retention
  ON session_control_deliveries (updated_at, delivery_id)
  WHERE state IN ('succeeded', 'expired', 'superseded');
CREATE INDEX IF NOT EXISTS idx_control_events_retention
  ON control_events (GREATEST(created_at, expires_at), event_id);
CREATE INDEX IF NOT EXISTS idx_session_tokens_retention
  ON user_session_tokens (LEAST(expires_at, COALESCE(revoked_at, expires_at)), token_hash);
CREATE INDEX IF NOT EXISTS idx_user_sessions_retention
  ON user_sessions (GREATEST(last_seen_at, COALESCE(revoked_at, last_seen_at)), session_id);
