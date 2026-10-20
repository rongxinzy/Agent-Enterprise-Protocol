-- Mutable live-Pod evidence is not part of the immutable employee/SA identity.
-- Pre-upgrade rows have no verified Pod evidence and must fail closed until a
-- trusted lifecycle observer supplies a live set. Retired history is preserved.
ALTER TABLE model_access_workloads ADD COLUMN pod_uids jsonb NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(pod_uids)='array' AND jsonb_array_length(pod_uids)<=512);

DROP TRIGGER model_access_workload_update ON model_access_workloads;
CREATE TRIGGER model_access_workload_update BEFORE UPDATE ON model_access_workloads
  FOR EACH ROW WHEN (NEW.retired_at IS DISTINCT FROM OLD.retired_at
    OR NEW.pod_uids IS DISTINCT FROM OLD.pod_uids
    OR NEW.verified_until<OLD.verified_until OR OLD.verified_until<=NEW.verified_at)
  EXECUTE FUNCTION capture_model_access_change();

-- Re-evaluate existing active rows with absent evidence. Only the affected
-- employee is queued; the source-version/configuration fences remain unchanged.
SELECT queue_model_access_change(deployment_id,'user',user_id)
FROM model_access_workloads WHERE retired_at IS NULL;
