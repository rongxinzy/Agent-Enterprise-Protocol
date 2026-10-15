-- Claims expire after worker crashes; the token fences database acknowledgements.
-- Kubernetes writes are independently fenced by monotone per-object sequence
-- markers and resourceVersion, including retained disabled-route tombstones.
ALTER TABLE data_plane_publication_outbox
  ADD COLUMN claim_token uuid,
  ADD COLUMN lease_until timestamptz,
  ADD COLUMN attempts bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN completed_at timestamptz,
  ADD CONSTRAINT publication_claim_pair CHECK ((claim_token IS NULL) = (lease_until IS NULL));

CREATE INDEX publication_outbox_pending
  ON data_plane_publication_outbox(deployment_id, next_attempt_at)
  WHERE completed_at IS NULL;
