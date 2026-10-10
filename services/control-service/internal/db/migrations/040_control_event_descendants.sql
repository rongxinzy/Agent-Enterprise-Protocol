-- Team-scoped control events may address a whole department subtree. scope_id
-- always names exactly one team; include_descendants records whether the
-- fan-out and the login backfill must expand to that team's children too.
-- Default false keeps every existing event's exact-team behaviour.
ALTER TABLE control_events ADD COLUMN IF NOT EXISTS include_descendants boolean NOT NULL DEFAULT false;
