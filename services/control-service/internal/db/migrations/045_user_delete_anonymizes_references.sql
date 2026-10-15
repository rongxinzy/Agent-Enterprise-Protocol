-- Deleting a user must not be blocked by the rows that reference it.
--
-- Five tables point at users(id) with the default NO ACTION rule, so a single
-- historical row made DELETE /admin/users fail with a foreign key violation:
-- the account could not be removed at all once it had produced telemetry, a
-- credential resolution audit row, a skill sync result, a control event or a
-- data-scope rule.
--
-- The rule becomes ON DELETE SET NULL, never ON DELETE CASCADE. An audit trail
-- that disappears together with the account it audits is not an audit trail,
-- and cascading a control event or a data-scope rule would silently change the
-- queued work and the authorization policy of the deployment. The row is kept
-- and only the actor reference is anonymized: after the deletion, user_id (or
-- created_by) is NULL, which reads as "the author's account no longer exists".
--
-- Constraint names differ between installs (sqlc names, renamed columns, the
-- historical enterprises/agents cutover), so discover them from PostgreSQL
-- metadata instead of assuming one name.
DO $$
DECLARE
  target_table text;
  target_column text;
  constraint_name text;
BEGIN
  FOR target_table, target_column IN
    SELECT * FROM (VALUES
      ('telemetry_events', 'user_id'),
      ('credential_resolution_audit', 'user_id'),
      ('skill_sync_results', 'user_id'),
      ('control_events', 'created_by'),
      ('data_scope_rules', 'created_by')
    ) AS targets(table_name, column_name)
  LOOP
    IF to_regclass(target_table) IS NULL THEN
      CONTINUE;
    END IF;
    FOR constraint_name IN
      SELECT con.conname
      FROM pg_constraint con
      JOIN pg_class rel ON rel.oid = con.conrelid
      JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = ANY (con.conkey)
      WHERE con.contype = 'f'
        AND rel.relnamespace = 'public'::regnamespace
        AND rel.relname = target_table
        AND att.attname = target_column
        AND con.confrelid = 'users'::regclass
    LOOP
      EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', target_table, constraint_name);
    END LOOP;
    -- The reference stays mandatory for new rows: only the deletion of the
    -- referenced user nulls it, and the API still writes a user for every row.
    EXECUTE format('ALTER TABLE %I ALTER COLUMN %I DROP NOT NULL', target_table, target_column);
    EXECUTE format(
      'ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY (%I) REFERENCES users(id) ON DELETE SET NULL',
      target_table, target_table || '_' || target_column || '_fkey', target_column);
  END LOOP;
END $$;
