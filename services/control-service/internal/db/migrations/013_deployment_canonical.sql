-- Canonicalize tenant naming while retaining legacy tables and constraints for
-- one release. Later migrations will remove the compatibility surface after
-- all runtime consumers have moved to Deployment, User, Role, and Team.

DO $$
DECLARE
  target_table text;
BEGIN
  FOREACH target_table IN ARRAY ARRAY[
    'organizations', 'users', 'agents', 'refresh_sessions',
    'skill_assignments', 'credentials', 'credential_assignments',
    'credential_resolution_audit', 'models', 'model_assignments',
    'control_events', 'control_deliveries', 'telemetry_events',
    'skill_sync_results', 'data_plane_desired_states', 'data_plane_statuses',
    'authentication_audit_events', 'license_activations', 'license_audit_events'
  ] LOOP
    IF to_regclass(target_table) IS NOT NULL
       AND EXISTS (
         SELECT 1 FROM information_schema.columns c
         WHERE c.table_schema='public' AND c.table_name=target_table
           AND c.column_name='enterprise_id'
       )
       AND NOT EXISTS (
         SELECT 1 FROM information_schema.columns c
         WHERE c.table_schema='public' AND c.table_name=target_table
           AND c.column_name='deployment_id'
       ) THEN
      EXECUTE format('ALTER TABLE %I RENAME COLUMN enterprise_id TO deployment_id', target_table);
    END IF;
  END LOOP;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='users' AND column_name='organization_ids'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='users' AND column_name='team_ids'
  ) THEN
    ALTER TABLE users RENAME COLUMN organization_ids TO team_ids;
  END IF;
END $$;

-- Licenses already carried a deployment key in the v1 envelope. Keep the
-- legacy enterprise key for this compatibility migration, but backfill the
-- canonical value for existing rows.
UPDATE licenses
SET deployment_id = enterprise_id
WHERE (deployment_id IS NULL OR deployment_id = '')
  AND enterprise_id IS NOT NULL;

-- Ensure every legacy enterprise has a matching deployment identity.
INSERT INTO deployments (id, name)
SELECT id, name FROM enterprises
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name;

-- Keep legacy assignment subjects valid during the API compatibility window.
-- PR #53 will migrate remaining grants and restore the RBAC-only checks.
ALTER TABLE skill_assignments DROP CONSTRAINT IF EXISTS skill_assignments_subject_type_check;
ALTER TABLE skill_assignments ADD CONSTRAINT skill_assignments_subject_type_check
  CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent', 'role', 'team'));
ALTER TABLE model_assignments DROP CONSTRAINT IF EXISTS model_assignments_subject_type_check;
ALTER TABLE model_assignments ADD CONSTRAINT model_assignments_subject_type_check
  CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent', 'role', 'team'));
ALTER TABLE credential_assignments DROP CONSTRAINT IF EXISTS credential_assignments_subject_type_check;
ALTER TABLE credential_assignments ADD CONSTRAINT credential_assignments_subject_type_check
  CHECK (subject_type IN ('enterprise', 'organization', 'user', 'agent', 'role', 'team'));
