-- Credential-free lifecycle observations. Only a trusted Kubernetes lifecycle
-- writer may register these; a browser, employee header or CR claim is no proof.
-- Keep retired identities after account deletion, like publication stream fences.
CREATE TABLE model_access_workloads (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  binding_key text NOT NULL CHECK (binding_key ~ '^[a-f0-9]{64}$'),
  user_id text NOT NULL CHECK (length(user_id) BETWEEN 1 AND 256),
  namespace text NOT NULL CHECK (length(namespace) BETWEEN 1 AND 63),
  employee_name text NOT NULL CHECK (length(employee_name) BETWEEN 1 AND 253),
  employee_uid text NOT NULL CHECK (length(employee_uid) BETWEEN 1 AND 128),
  service_account_name text NOT NULL CHECK (length(service_account_name) BETWEEN 1 AND 253),
  service_account_uid text NOT NULL UNIQUE CHECK (length(service_account_uid) BETWEEN 1 AND 128),
  audience text NOT NULL CHECK (length(audience) BETWEEN 1 AND 256),
  verified_at timestamptz NOT NULL,
  verified_until timestamptz NOT NULL,
  retired_at timestamptz,
  PRIMARY KEY (deployment_id,binding_key),
  CHECK (verified_until>verified_at AND verified_until<=verified_at+interval '1 hour')
);
CREATE UNIQUE INDEX model_access_workload_current_subject
  ON model_access_workloads(deployment_id,user_id) WHERE retired_at IS NULL;
CREATE UNIQUE INDEX model_access_workload_current_employee
  ON model_access_workloads(employee_uid) WHERE retired_at IS NULL;
CREATE INDEX model_access_workload_employee_history ON model_access_workloads(employee_uid);

CREATE FUNCTION protect_model_access_workload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    IF EXISTS(SELECT 1 FROM deployments WHERE id=OLD.deployment_id) THEN
      RAISE EXCEPTION 'workload identity fences must be retained' USING ERRCODE='23514';
    END IF;
    RETURN OLD;
  END IF;
  IF ROW(NEW.deployment_id,NEW.binding_key,NEW.user_id,NEW.namespace,NEW.employee_name,
      NEW.employee_uid,NEW.service_account_name,NEW.service_account_uid,NEW.audience)
    IS DISTINCT FROM
    ROW(OLD.deployment_id,OLD.binding_key,OLD.user_id,OLD.namespace,OLD.employee_name,
      OLD.employee_uid,OLD.service_account_name,OLD.service_account_uid,OLD.audience)
    OR OLD.retired_at IS NOT NULL THEN
    RAISE EXCEPTION 'workload identity is immutable or retired' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER model_access_workload_fence BEFORE UPDATE OR DELETE ON model_access_workloads
  FOR EACH ROW EXECUTE FUNCTION protect_model_access_workload();

CREATE TRIGGER model_access_workload_insert BEFORE INSERT ON model_access_workloads
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
-- Renewing a continuously verified identity is not a permission change/cache
-- invalidation. Shorter bounds, recovery after expiry and retirement are work.
CREATE TRIGGER model_access_workload_update BEFORE UPDATE ON model_access_workloads
  FOR EACH ROW WHEN (NEW.retired_at IS DISTINCT FROM OLD.retired_at
    OR NEW.verified_until<OLD.verified_until OR OLD.verified_until<=NEW.verified_at)
  EXECUTE FUNCTION capture_model_access_change();

-- Account deletion withdraws live bindings but keeps immutable ownership fences.
-- A recreated account must not inherit an old account's workload observation.
CREATE FUNCTION retire_model_access_workloads_for_user() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE model_access_workloads SET retired_at=clock_timestamp()
    WHERE deployment_id=OLD.deployment_id AND user_id=OLD.id AND retired_at IS NULL;
  RETURN OLD;
END;
$$;
CREATE TRIGGER model_access_workload_user_delete AFTER DELETE ON users
  FOR EACH ROW EXECUTE FUNCTION retire_model_access_workloads_for_user();
