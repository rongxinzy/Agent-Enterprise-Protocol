-- The audit view filters by deployment and orders by occurred_at; the existing
-- indexes all lead with session, user or type, so a deployment-wide time-range
-- query had no usable index.
CREATE INDEX IF NOT EXISTS idx_telemetry_deployment_time
  ON telemetry_events (deployment_id, occurred_at DESC, event_id DESC);
