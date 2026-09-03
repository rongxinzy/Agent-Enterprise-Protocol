-- Session-scoped control delivery and telemetry. Legacy Agent tables remain
-- during the migration window and are removed in the final identity cutover.
CREATE TABLE IF NOT EXISTS session_control_deliveries (
  cursor bigserial UNIQUE,
  delivery_id text PRIMARY KEY,
  event_id text NOT NULL REFERENCES control_events(event_id) ON DELETE CASCADE,
  session_id text NOT NULL REFERENCES user_sessions(session_id) ON DELETE CASCADE,
  state text NOT NULL DEFAULT 'pending',
  attempt_count integer NOT NULL DEFAULT 0,
  received_at timestamptz,
  started_at timestamptz,
  completed_at timestamptz,
  applied_revision text,
  error_code text,
  message text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (event_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_session_deliveries_session_state
  ON session_control_deliveries (session_id, state, cursor);
CREATE INDEX IF NOT EXISTS idx_session_deliveries_event
  ON session_control_deliveries (event_id, cursor);

ALTER TABLE telemetry_events ADD COLUMN IF NOT EXISTS session_id text REFERENCES user_sessions(session_id) ON DELETE SET NULL;
ALTER TABLE telemetry_events ALTER COLUMN agent_id DROP NOT NULL;
CREATE INDEX IF NOT EXISTS idx_telemetry_session_search
  ON telemetry_events (enterprise_id, session_id, occurred_at DESC);

-- Team is the RBAC replacement for the former organization event scope.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'control_events_scope_type_check') THEN
    ALTER TABLE control_events DROP CONSTRAINT control_events_scope_type_check;
  END IF;
  ALTER TABLE control_events
    ADD CONSTRAINT control_events_scope_type_check
    CHECK (scope_type IN ('global', 'team', 'user', 'organization', 'agent'));
END $$;
