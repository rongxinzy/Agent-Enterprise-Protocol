-- Durable, coalescing inputs for the existing publication relay. This table is
-- NOT an authorization source or a publication acknowledgement. A signal and
-- its business change commit/roll back together. Keep deleted user IDs so the
-- relay can publish deny tombstones; only deleting the deployment removes them.
CREATE TABLE model_access_changes (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  user_id text NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  acknowledged_version bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, user_id),
  CHECK (acknowledged_version >= 0 AND acknowledged_version <= version)
);
CREATE INDEX model_access_changes_pending_idx
  ON model_access_changes (deployment_id, user_id)
  WHERE version > acknowledged_version;
CREATE INDEX model_access_role_members_idx
  ON user_role_bindings (deployment_id, role_id, user_id);
CREATE INDEX model_access_team_members_idx
  ON user_team_bindings (deployment_id, team_id, user_id);

-- Dependency expansion, not grant evaluation: deliberately include disabled
-- roles/teams so removing a former effective membership is never overlooked.
-- user_effective_roles remains the enabled-role authorization source. Legacy
-- user/team assignment dependencies are captured for safe cutover/revocation;
-- they must not become new direct grants in ModelAccessGrant publications.
CREATE FUNCTION model_access_members(dep text, kind text, subject text)
RETURNS TABLE(user_id text) LANGUAGE sql STABLE AS $$
  SELECT $3 WHERE $2='user'
  UNION
  SELECT b.user_id FROM user_role_bindings b
    WHERE $2='role' AND b.deployment_id=$1 AND b.role_id=$3
  UNION
  SELECT b.user_id FROM user_team_bindings b
    JOIN team_default_roles d ON d.deployment_id=b.deployment_id AND d.team_id=b.team_id
    WHERE $2='role' AND d.deployment_id=$1 AND d.role_id=$3
  UNION
  SELECT b.user_id FROM user_team_bindings b
    WHERE $2='team' AND b.deployment_id=$1 AND b.team_id=$3
  UNION
  SELECT u.id FROM users u WHERE $2='deployment' AND u.deployment_id=$1;
$$;

CREATE FUNCTION queue_model_access_change(dep text, kind text, subject text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  -- Serialize dependency fanout with concurrent membership/grant writes. Reuse
  -- the existing gateway configuration transaction lock; otherwise a new member
  -- could be acknowledged against old policy while a concurrent fanout misses
  -- its not-yet-committed binding. The following SPI query gets a fresh snapshot
  -- after waiting for this lock (this function is VOLATILE, not STABLE).
  PERFORM pg_advisory_xact_lock(hashtextextended(dep,101));
  -- Deployment cascades need neither new signals nor orphan tombstones.
  IF NOT EXISTS(SELECT 1 FROM deployments WHERE id=dep) THEN RETURN; END IF;
  INSERT INTO model_access_changes(deployment_id,user_id)
    SELECT dep,affected.user_id FROM (
      SELECT user_id FROM model_access_members(dep,kind,subject)
      UNION
      SELECT members.user_id FROM model_assignments a
        CROSS JOIN LATERAL model_access_members(dep,a.subject_type,a.subject_id) members
        WHERE kind='model' AND a.deployment_id=dep AND a.model_id=subject
    ) affected ORDER BY affected.user_id
    ON CONFLICT(deployment_id,user_id) DO UPDATE
      SET version=model_access_changes.version+1,updated_at=now();
END;
$$;

CREATE FUNCTION queue_model_access_record(source_table text, value jsonb)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  kind text;
  subject text;
BEGIN
  CASE source_table
    WHEN 'users' THEN kind:='user'; subject:=value->>'id';
    WHEN 'roles' THEN kind:='role'; subject:=value->>'id';
    WHEN 'teams' THEN kind:='team'; subject:=value->>'id';
    WHEN 'models' THEN kind:='model'; subject:=value->>'id';
    WHEN 'role_permissions' THEN kind:='role'; subject:=value->>'role_id';
    WHEN 'team_default_roles' THEN kind:='team'; subject:=value->>'team_id';
    WHEN 'model_assignments' THEN kind:=value->>'subject_type'; subject:=value->>'subject_id';
    WHEN 'licenses', 'license_activations' THEN kind:='deployment'; subject:=value->>'deployment_id';
    ELSE kind:='user'; subject:=value->>'user_id';
  END CASE;
  PERFORM queue_model_access_change(value->>'deployment_id',kind,subject);
END;
$$;

CREATE FUNCTION capture_model_access_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN
    PERFORM queue_model_access_record(TG_TABLE_NAME,to_jsonb(OLD));
  END IF;
  IF TG_OP <> 'DELETE' THEN
    -- Both old and new dependencies matter when an identity/binding moves.
    -- Versions coalesce work; they are not event counts or publication IDs.
    PERFORM queue_model_access_record(TG_TABLE_NAME,to_jsonb(NEW));
    RETURN NEW;
  END IF;
  RETURN OLD;
END;
$$;

-- Capture parents BEFORE DELETE, while memberships/assignments still exist.
CREATE TRIGGER model_access_user_delete BEFORE DELETE ON users
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_role_delete BEFORE DELETE ON roles
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_team_delete BEFORE DELETE ON teams
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_model_delete BEFORE DELETE ON models
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();

CREATE TRIGGER model_access_user_insert BEFORE INSERT ON users
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_user_update BEFORE UPDATE ON users
  FOR EACH ROW WHEN (
    ROW(OLD.deployment_id,OLD.id,OLD.kind,OLD.status,OLD.password_hash,OLD.require_password_change,OLD.is_admin)
    IS DISTINCT FROM
    ROW(NEW.deployment_id,NEW.id,NEW.kind,NEW.status,NEW.password_hash,NEW.require_password_change,NEW.is_admin)
  ) EXECUTE FUNCTION capture_model_access_change();

CREATE TRIGGER model_access_role_update BEFORE UPDATE ON roles
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.id,OLD.enabled) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.id,NEW.enabled))
  EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_team_update BEFORE UPDATE ON teams
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.id,OLD.enabled) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.id,NEW.enabled))
  EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_model_update BEFORE UPDATE ON models
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.id,OLD.enabled) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.id,NEW.enabled))
  EXECUTE FUNCTION capture_model_access_change();

CREATE TRIGGER model_access_direct_role_write BEFORE INSERT OR DELETE ON user_role_bindings
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_direct_role_update BEFORE UPDATE ON user_role_bindings
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.user_id,OLD.role_id) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.user_id,NEW.role_id))
  EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_team_member_write BEFORE INSERT OR DELETE ON user_team_bindings
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_team_member_update BEFORE UPDATE ON user_team_bindings
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.user_id,OLD.team_id) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.user_id,NEW.team_id))
  EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_team_default_write BEFORE INSERT OR DELETE ON team_default_roles
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_team_default_update BEFORE UPDATE ON team_default_roles
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.team_id,OLD.role_id) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.team_id,NEW.role_id))
  EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_role_permission_write BEFORE INSERT OR DELETE ON role_permissions
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_role_permission_update BEFORE UPDATE ON role_permissions
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.role_id,OLD.permission_id) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.role_id,NEW.permission_id))
  EXECUTE FUNCTION capture_model_access_change();

CREATE TRIGGER model_access_assignment_write BEFORE INSERT OR DELETE ON model_assignments
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_assignment_update BEFORE UPDATE ON model_assignments
  FOR EACH ROW WHEN (
    ROW(OLD.deployment_id,OLD.model_id,OLD.subject_type,OLD.subject_id,OLD.expires_at)
    IS DISTINCT FROM ROW(NEW.deployment_id,NEW.model_id,NEW.subject_type,NEW.subject_id,NEW.expires_at)
  ) EXECUTE FUNCTION capture_model_access_change();

CREATE TRIGGER model_access_session_write BEFORE INSERT OR DELETE ON user_sessions
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_session_update BEFORE UPDATE ON user_sessions
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.user_id,OLD.session_id,OLD.revoked_at) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.user_id,NEW.session_id,NEW.revoked_at))
  EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_profile_write BEFORE INSERT OR DELETE ON agent_profiles
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_profile_update BEFORE UPDATE ON agent_profiles
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.user_id,OLD.ephemeral,OLD.expires_at) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.user_id,NEW.ephemeral,NEW.expires_at))
  EXECUTE FUNCTION capture_model_access_change();

-- A license changes every principal in its deployment, never another tenant.
-- Heartbeats (activation last_seen_at) and token rotation are not policy changes.
CREATE TRIGGER model_access_license_write BEFORE INSERT OR DELETE ON licenses
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_license_update BEFORE UPDATE ON licenses
  FOR EACH ROW WHEN (
    ROW(OLD.deployment_id,OLD.status,OLD.digest,OLD.expires_at,OLD.grace_ends_at,OLD.features,OLD.payload)
    IS DISTINCT FROM ROW(NEW.deployment_id,NEW.status,NEW.digest,NEW.expires_at,NEW.grace_ends_at,NEW.features,NEW.payload)
  ) EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_activation_write BEFORE INSERT OR DELETE ON license_activations
  FOR EACH ROW EXECUTE FUNCTION capture_model_access_change();
CREATE TRIGGER model_access_activation_update BEFORE UPDATE ON license_activations
  FOR EACH ROW WHEN (ROW(OLD.deployment_id,OLD.license_id,OLD.revoked_at) IS DISTINCT FROM ROW(NEW.deployment_id,NEW.license_id,NEW.revoked_at))
  EXECUTE FUNCTION capture_model_access_change();

-- Seed existing principals once. Later relay captures must compare canonical
-- effective output: a signal alone must never flush an unchanged warm result.
INSERT INTO model_access_changes(deployment_id,user_id)
  SELECT deployment_id,id FROM users ORDER BY deployment_id,id;
