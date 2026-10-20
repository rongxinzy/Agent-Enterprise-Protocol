-- Natural expiry and publication freshness are durable producer work, not
-- business-write events. Never depend on an in-memory timer surviving a crash.
-- Requires the targeted-signal stage (052) before this integration stage.
ALTER TABLE model_access_changes ADD COLUMN next_capture_at timestamptz;
CREATE INDEX model_access_capture_due_idx
  ON model_access_changes (deployment_id, next_capture_at, user_id)
  WHERE next_capture_at IS NOT NULL;
