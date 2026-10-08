-- Active model health probes and the deployment failover chain.
--
-- health_status is written by the periodic model-health prober: it calls the
-- upstream endpoint with the stored credential and classifies reachability,
-- credential validity, and upstream model availability. The other columns
-- stay null until the first probe. health_since marks when the current status
-- began (set only on transitions), which lets consumers derive hysteresis —
-- e.g. fail an employee over only after N minutes of continuous unhealth.
--
-- model_fallback_ids is the ordered, deployment-configurable failover chain:
-- NULL means "no override" (the AEP_MODEL_FALLBACK_IDS environment value
-- applies), an explicit empty array disables automatic failover.

ALTER TABLE models
  ADD COLUMN IF NOT EXISTS health_status text NOT NULL DEFAULT 'unknown'
    CHECK (health_status IN ('unknown', 'healthy', 'credential_invalid', 'model_missing', 'unreachable', 'error')),
  ADD COLUMN IF NOT EXISTS health_checked_at timestamptz,
  ADD COLUMN IF NOT EXISTS health_since timestamptz,
  ADD COLUMN IF NOT EXISTS health_detail text;

ALTER TABLE deployment_settings
  ADD COLUMN IF NOT EXISTS model_fallback_ids text[];
