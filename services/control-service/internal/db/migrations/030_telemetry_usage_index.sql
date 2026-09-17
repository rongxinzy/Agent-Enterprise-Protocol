-- Model usage telemetry: usage rides the existing telemetry_events stream as
-- type 'model.usage'. The Token Ledger consumes it by type with a time
-- cursor, so the index leads with deployment, type, then time.
CREATE INDEX IF NOT EXISTS idx_telemetry_type_time
  ON telemetry_events (deployment_id, type, occurred_at DESC);
