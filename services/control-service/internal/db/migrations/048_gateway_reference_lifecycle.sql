-- Identity/catalog deletion and native rule invalidation are one transaction.
-- Retain a permanent fence when name-derived role/team/model IDs are reused.
ALTER TABLE gateway_limits ADD COLUMN reference_invalidated boolean NOT NULL DEFAULT false;
ALTER TABLE gateway_limits ADD CONSTRAINT gateway_reference_tombstone
  CHECK (NOT reference_invalidated OR deleted);

-- A coalescing transactional outbox for the publication worker. It records no
-- credential or customer payload; acknowledging one version cannot lose a
-- concurrent deletion. Existing publication snapshots remain immutable.
CREATE TABLE gateway_reference_invalidations (
  deployment_id text PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  acknowledged_version bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (acknowledged_version >= 0 AND acknowledged_version <= version)
);

CREATE TABLE gateway_model_removals (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  model_id text NOT NULL,
  last_publication_sequence bigint NOT NULL,
  PRIMARY KEY(deployment_id,model_id)
);

CREATE FUNCTION invalidate_gateway_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  scope_kind text;
  affected bigint;
BEGIN
  -- A deployment-wide cascade needs no replacement intent for a removed owner.
  IF NOT EXISTS(SELECT 1 FROM deployments WHERE id=OLD.deployment_id) THEN RETURN OLD; END IF;
  scope_kind := CASE TG_TABLE_NAME WHEN 'users' THEN 'user' WHEN 'teams' THEN 'team'
    WHEN 'roles' THEN 'role' WHEN 'models' THEN 'model' END;
  -- Same lock as native rule PUT/DELETE/capture. The writer's existence read
  -- happens after this lock, closing the check-then-create/delete race.
  PERFORM pg_advisory_xact_lock(hashtextextended(OLD.deployment_id,101));
  UPDATE gateway_limits SET deleted=true, reference_invalidated=true,
    version=version+1, configuration=jsonb_set(configuration,'{enabled}','false'), updated_at=now()
  WHERE deployment_id=OLD.deployment_id AND NOT reference_invalidated AND
    ((configuration->>'scopeType'=scope_kind AND configuration->>'scopeId'=OLD.id)
      OR (scope_kind='model' AND configuration->>'modelId'=OLD.id));
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF scope_kind='model' THEN
    INSERT INTO gateway_model_removals(deployment_id,model_id,last_publication_sequence)
      SELECT OLD.deployment_id,OLD.id,COALESCE(MAX(sequence),0) FROM data_plane_publications WHERE deployment_id=OLD.deployment_id
      ON CONFLICT(deployment_id,model_id) DO UPDATE SET last_publication_sequence=EXCLUDED.last_publication_sequence;
  END IF;
  IF affected > 0 OR scope_kind='model' THEN
    INSERT INTO gateway_reference_invalidations(deployment_id) VALUES(OLD.deployment_id)
      ON CONFLICT(deployment_id) DO UPDATE SET version=gateway_reference_invalidations.version+1,updated_at=now();
  END IF;
  RETURN OLD;
END;
$$;

CREATE TRIGGER gateway_user_reference BEFORE DELETE ON users
  FOR EACH ROW EXECUTE FUNCTION invalidate_gateway_reference();
CREATE TRIGGER gateway_team_reference BEFORE DELETE ON teams
  FOR EACH ROW EXECUTE FUNCTION invalidate_gateway_reference();
CREATE TRIGGER gateway_role_reference BEFORE DELETE ON roles
  FOR EACH ROW EXECUTE FUNCTION invalidate_gateway_reference();
CREATE TRIGGER gateway_model_reference BEFORE DELETE ON models
  FOR EACH ROW EXECUTE FUNCTION invalidate_gateway_reference();

-- Fence orphaned rules left by older versions as part of the same migration.
WITH invalidated AS (
  UPDATE gateway_limits g SET deleted=true,reference_invalidated=true,version=version+1,
    configuration=jsonb_set(configuration,'{enabled}','false'),updated_at=now()
  WHERE NOT reference_invalidated AND (
    (configuration->>'scopeType'='user' AND NOT EXISTS(SELECT 1 FROM users u WHERE u.deployment_id=g.deployment_id AND u.id=g.configuration->>'scopeId')) OR
    (configuration->>'scopeType'='team' AND NOT EXISTS(SELECT 1 FROM teams t WHERE t.deployment_id=g.deployment_id AND t.id=g.configuration->>'scopeId')) OR
    (configuration->>'scopeType'='role' AND NOT EXISTS(SELECT 1 FROM roles r WHERE r.deployment_id=g.deployment_id AND r.id=g.configuration->>'scopeId')) OR
    (configuration->>'scopeType'='model' AND NOT EXISTS(SELECT 1 FROM models m WHERE m.deployment_id=g.deployment_id AND m.id=g.configuration->>'scopeId')) OR
    (configuration->>'modelId' IS NOT NULL AND NOT EXISTS(SELECT 1 FROM models m WHERE m.deployment_id=g.deployment_id AND m.id=g.configuration->>'modelId')))
  RETURNING deployment_id
)
INSERT INTO gateway_reference_invalidations(deployment_id)
  SELECT DISTINCT deployment_id FROM invalidated
  ON CONFLICT(deployment_id) DO UPDATE SET version=gateway_reference_invalidations.version+1,updated_at=now();
