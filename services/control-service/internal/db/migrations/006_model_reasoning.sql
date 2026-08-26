ALTER TABLE models
  ADD COLUMN IF NOT EXISTS reasoning_compatibility jsonb;

ALTER TABLE models
  DROP CONSTRAINT IF EXISTS models_reasoning_compatibility_object;
ALTER TABLE models
  ADD CONSTRAINT models_reasoning_compatibility_object
  CHECK (reasoning_compatibility IS NULL OR jsonb_typeof(reasoning_compatibility) = 'object');
