-- Operator reference configuration; no numeric cost processing or runtime publication.
CREATE TABLE model_pricing (
  deployment_id text NOT NULL,
  model_id text NOT NULL,
  version bigint NOT NULL CHECK (version > 0 AND version <= 9007199254740991),
  pricing jsonb CHECK (pricing IS NULL OR jsonb_typeof(pricing) = 'object'),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, model_id),
  FOREIGN KEY (deployment_id, model_id) REFERENCES models(deployment_id, id) ON DELETE CASCADE
);
