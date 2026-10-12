-- The audit view filters by deployment and orders by occurred_at; the existing
-- indexes all lead with session, user or type, so a deployment-wide time-range
-- query had no usable index.
--
-- This runs inside the migrator's transaction, so it is a plain CREATE INDEX:
-- CONCURRENTLY cannot run in a transaction and would fail. Building the index
-- briefly blocks writes to telemetry_events; the table is small at this stage.
CREATE INDEX IF NOT EXISTS idx_telemetry_deployment_time
  ON telemetry_events (deployment_id, occurred_at DESC, event_id DESC);
