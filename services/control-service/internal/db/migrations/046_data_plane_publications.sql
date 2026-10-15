-- Durable operator publication intents. Separate from the legacy desired-state
-- tables: this stage does not start a second writer of Higress resources.
CREATE TABLE data_plane_publication_snapshots (
  deployment_id text NOT NULL REFERENCES deployments(id),
  revision text NOT NULL CHECK (length(revision) BETWEEN 1 AND 200),
  content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
  payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, revision),
  UNIQUE (deployment_id, revision, content_hash)
);

CREATE TABLE data_plane_publications (
  deployment_id text NOT NULL REFERENCES deployments(id),
  operation_id uuid NOT NULL,
  sequence bigserial NOT NULL,
  idempotency_key_hash text NOT NULL CHECK (idempotency_key_hash ~ '^[a-f0-9]{64}$'),
  request_hash text NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  revision text NOT NULL,
  content_hash text NOT NULL,
  phase text NOT NULL DEFAULT 'queued' CHECK (phase IN ('queued','projecting','projected','failed','superseded')),
  error_code text CHECK (error_code ~ '^[A-Z][A-Z0-9_]{0,99}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  idempotency_expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
  PRIMARY KEY (deployment_id, operation_id),
  UNIQUE (deployment_id, idempotency_key_hash),
  UNIQUE (deployment_id, sequence),
  FOREIGN KEY (deployment_id, revision, content_hash)
    REFERENCES data_plane_publication_snapshots(deployment_id, revision, content_hash),
  CHECK (idempotency_expires_at >= created_at + interval '24 hours'),
  CHECK ((phase = 'failed') = (error_code IS NOT NULL))
);

-- The outbox references the immutable snapshot through the operation. No raw
-- Secret or Credential values are resolved or copied by this path.
CREATE TABLE data_plane_publication_outbox (
  deployment_id text NOT NULL,
  operation_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, operation_id),
  FOREIGN KEY (deployment_id, operation_id)
    REFERENCES data_plane_publications(deployment_id, operation_id)
);

-- No automatic GC in this stage. In particular, never delete pending intents
-- just because the minimum idempotency-retention window has elapsed.
