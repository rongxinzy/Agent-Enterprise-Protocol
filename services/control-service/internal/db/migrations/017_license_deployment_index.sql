-- Dropping the legacy enterprise_id column in migration 015 also removed its
-- status index. Recreate the canonical lookup index on deployment_id.
CREATE INDEX IF NOT EXISTS idx_licenses_deployment_status
  ON licenses (deployment_id, status, expires_at);
