-- Final identity cutover. Deployment is the only installation boundary;
-- User/Role/Team are the only business and authorization subjects. A terminal
-- is represented by user_sessions and its durable session delivery cursor.

-- Drop every historical foreign key that still points at the removed
-- enterprises/agents tables. Constraint names differ between old installs, so
-- discover them from PostgreSQL metadata instead of assuming sqlc names.
DO $$
DECLARE constraint_row record;
BEGIN
  FOR constraint_row IN
    SELECT DISTINCT tc.table_name, tc.constraint_name
    FROM information_schema.table_constraints tc
    JOIN information_schema.constraint_column_usage ccu
      ON ccu.constraint_name=tc.constraint_name
     AND ccu.table_schema=tc.table_schema
    WHERE tc.table_schema='public'
      AND tc.constraint_type='FOREIGN KEY'
      AND ccu.table_name IN ('enterprises','agents')
  LOOP
    EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', constraint_row.table_name, constraint_row.constraint_name);
  END LOOP;
END $$;

-- Normalize the remaining resource tables to the Deployment root.
DO $$
DECLARE target_table text;
BEGIN
  FOREACH target_table IN ARRAY ARRAY[
    'users','skill_assignments','credentials','credential_assignments',
    'credential_resolution_audit','models','model_assignments','control_events',
    'telemetry_events','skill_sync_results','data_plane_desired_states',
    'data_plane_statuses','license_activations','license_audit_events'
  ] LOOP
    IF to_regclass(target_table) IS NOT NULL THEN
      BEGIN
        EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY (deployment_id) REFERENCES deployments(id) ON DELETE CASCADE', target_table, target_table || '_deployment_fkey');
      EXCEPTION WHEN duplicate_object THEN NULL;
      END;
    END IF;
  END LOOP;
END $$;

-- Legacy arrays are no longer an authorization source. Bindings are the
-- canonical membership relation and are already backfilled by migration 010.
ALTER TABLE users DROP COLUMN IF EXISTS organization_ids;
ALTER TABLE users DROP COLUMN IF EXISTS team_ids;
ALTER TABLE users DROP COLUMN IF EXISTS role_ids;

-- Credential delivery targets a signed-in client session, not an Agent
-- identity. Preserve existing rows while moving the enum vocabulary.
UPDATE credentials SET delivery_mode='client' WHERE delivery_mode='agent';
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_delivery_mode_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_delivery_mode_check
  CHECK (delivery_mode IN ('server_only','client'));

ALTER TABLE credential_resolution_audit DROP COLUMN IF EXISTS agent_id;
ALTER TABLE telemetry_events DROP COLUMN IF EXISTS agent_id;
ALTER TABLE skill_sync_results DROP COLUMN IF EXISTS agent_id;
ALTER TABLE authentication_audit_events DROP COLUMN IF EXISTS agent_id;

-- License activation is deployment-scoped. It records the user who performed
-- activation, never a machine/Agent identity.
ALTER TABLE licenses DROP COLUMN IF EXISTS enterprise_id;
ALTER TABLE licenses RENAME COLUMN agent_limit TO activation_limit;
ALTER TABLE license_activations DROP COLUMN IF EXISTS agent_id;
ALTER TABLE license_activations DROP CONSTRAINT IF EXISTS license_activations_license_id_agent_id_key;
DELETE FROM license_activations a
USING license_activations duplicate
WHERE a.license_id=duplicate.license_id
  AND a.deployment_id=duplicate.deployment_id
  AND a.id>duplicate.id;
ALTER TABLE license_activations
  ADD CONSTRAINT license_activations_license_deployment_key UNIQUE (license_id, deployment_id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname='control_events_scope_type_check') THEN
    ALTER TABLE control_events DROP CONSTRAINT control_events_scope_type_check;
  END IF;
  ALTER TABLE control_events ADD CONSTRAINT control_events_scope_type_check
    CHECK (scope_type IN ('global','team','user'));
END $$;

-- Remove obsolete storage after all data has been converted to sessions and
-- RBAC assignments. Historical migration files remain immutable for upgrades.
DROP TABLE IF EXISTS control_deliveries;
DROP TABLE IF EXISTS refresh_sessions;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS organizations;
DROP TABLE IF EXISTS enterprises;

CREATE INDEX IF NOT EXISTS idx_telemetry_user_time
  ON telemetry_events (deployment_id, user_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_authentication_audit_session_time
  ON authentication_audit_events (deployment_id, session_id, created_at DESC);
