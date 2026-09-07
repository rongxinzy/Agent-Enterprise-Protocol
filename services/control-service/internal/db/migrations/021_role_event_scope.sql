-- Role-scoped control events are delivered to every active session belonging
-- to a user with the addressed role.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'control_events_scope_type_check') THEN
    ALTER TABLE control_events DROP CONSTRAINT control_events_scope_type_check;
  END IF;
  ALTER TABLE control_events ADD CONSTRAINT control_events_scope_type_check
    CHECK (scope_type IN ('global','team','role','user'));
END $$;
