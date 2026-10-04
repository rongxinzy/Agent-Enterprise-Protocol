-- Query-path indexes for control_events (scale-round finding B3): the
-- admin listing orders by (deployment_id, created_at DESC) and the
-- supersede UPDATE filters on (deployment_id, supersedes_key) with
-- state='active' — both were sequential scans at 383-session scale.
CREATE INDEX IF NOT EXISTS idx_control_events_deployment_created
  ON control_events (deployment_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_control_events_supersedes_active
  ON control_events (deployment_id, supersedes_key) WHERE state='active';
