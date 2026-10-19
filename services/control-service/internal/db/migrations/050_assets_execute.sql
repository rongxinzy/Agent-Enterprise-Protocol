-- Global emergency stop for governed asset execution (FR-AS-11): false
-- stops every automated asset operation deployment-wide; NULL keeps the
-- default (enabled). Per-asset stop is the asset status (disabled).
ALTER TABLE deployment_settings ADD COLUMN IF NOT EXISTS assets_execute_enabled boolean;
