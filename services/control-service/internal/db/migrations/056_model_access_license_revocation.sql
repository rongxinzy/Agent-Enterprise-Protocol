-- The source excludes a revoked license even if status has not changed.
-- Signal the same predicate; changing revoked_at alone must not leave allow
-- publications current until their unrelated freshness timer happens to run.
DROP TRIGGER model_access_license_update ON licenses;
CREATE TRIGGER model_access_license_update BEFORE UPDATE ON licenses
  FOR EACH ROW WHEN (
    ROW(OLD.deployment_id,OLD.status,OLD.revoked_at,OLD.digest,OLD.expires_at,OLD.grace_ends_at,OLD.features,OLD.payload)
    IS DISTINCT FROM ROW(NEW.deployment_id,NEW.status,NEW.revoked_at,NEW.digest,NEW.expires_at,NEW.grace_ends_at,NEW.features,NEW.payload)
  ) EXECUTE FUNCTION capture_model_access_change();

-- Existing inconsistent rows also need a fresh deny after upgrade. Keep the
-- deployment-scoped fanout, configuration lock and version fence unchanged.
SELECT queue_model_access_change(deployment_id,'deployment',deployment_id)
FROM (SELECT DISTINCT deployment_id FROM licenses WHERE status='active' AND revoked_at IS NOT NULL) affected;
