-- The admin install view reads the newest sync report per user for one
-- deployment, but the only index on skill_sync_results leads with created_at,
-- so that DISTINCT ON had to read and sort every retained report of the
-- deployment. created_at alone is not a total order, so id breaks ties.
--
-- This runs inside the migrator's transaction (CONCURRENTLY cannot), so it
-- briefly blocks writes to skill_sync_results; the table is small at this stage.
CREATE INDEX IF NOT EXISTS idx_skill_sync_latest_per_user
  ON skill_sync_results (deployment_id, user_id, created_at DESC, id DESC);
