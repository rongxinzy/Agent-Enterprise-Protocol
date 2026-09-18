-- Assignment expiry: temporary grants must stop authorizing the moment they
-- expire. Entitlement evaluation filters expired rows eagerly; the retention
-- job only performs the physical delete.
ALTER TABLE skill_assignments ADD COLUMN expires_at timestamptz;
ALTER TABLE model_assignments ADD COLUMN expires_at timestamptz;
ALTER TABLE credential_assignments ADD COLUMN expires_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_skill_assignments_expiry
  ON skill_assignments (expires_at, id) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_model_assignments_expiry
  ON model_assignments (expires_at, id) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_credential_assignments_expiry
  ON credential_assignments (expires_at, id) WHERE expires_at IS NOT NULL;
