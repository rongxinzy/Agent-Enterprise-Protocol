-- Per-binding publication streams for the existing relay. This storage stage
-- is independent of signal capture (052); it does not enable a second writer.
-- Never reset these fences on user/session/CR deletion or identifier reuse.
CREATE TABLE model_access_streams (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  binding_key text NOT NULL CHECK (binding_key ~ '^[a-f0-9]{64}$'),
  subject_id text NOT NULL CHECK (length(subject_id) BETWEEN 1 AND 256),
  last_source_version bigint NOT NULL DEFAULT 0 CHECK (last_source_version >= 0),
  last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
  PRIMARY KEY(deployment_id,binding_key),
  UNIQUE(deployment_id,binding_key,subject_id)
);

CREATE TABLE model_access_publications (
  deployment_id text NOT NULL,
  binding_key text NOT NULL,
  subject_id text NOT NULL,
  operation_id uuid NOT NULL,
  sequence bigint NOT NULL CHECK (sequence > 0),
  source_version bigint NOT NULL CHECK (source_version > 0),
  revision text NOT NULL CHECK (length(revision) BETWEEN 1 AND 200),
  content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
  -- Preserve exact JSON bytes: jsonb normalization must not change a snapshot hash.
  payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 2 AND 1048576),
  issued_at timestamptz NOT NULL,
  valid_until timestamptz NOT NULL,
  phase text NOT NULL DEFAULT 'queued' CHECK (phase IN ('queued','projecting','projected','failed','superseded')),
  error_code text CHECK (error_code ~ '^[A-Z][A-Z0-9_]{0,99}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(deployment_id,operation_id),
  UNIQUE(deployment_id,binding_key,sequence),
  FOREIGN KEY(deployment_id,binding_key,subject_id) REFERENCES model_access_streams(deployment_id,binding_key,subject_id) ON DELETE CASCADE,
  CHECK (valid_until > issued_at AND valid_until <= issued_at+interval '1 hour'),
  CHECK ((phase='failed') = (error_code IS NOT NULL)),
  CHECK (jsonb_typeof(convert_from(payload,'UTF8')::jsonb)='object')
);

CREATE TABLE model_access_publication_outbox (
  deployment_id text NOT NULL,
  operation_id uuid NOT NULL,
  claim_token uuid,
  lease_until timestamptz,
  attempts bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY(deployment_id,operation_id),
  FOREIGN KEY(deployment_id,operation_id) REFERENCES model_access_publications(deployment_id,operation_id) ON DELETE CASCADE,
  CHECK ((claim_token IS NULL) = (lease_until IS NULL))
);
CREATE INDEX model_access_delivery_pending_idx ON model_access_publication_outbox(deployment_id,next_attempt_at)
  WHERE completed_at IS NULL;

CREATE FUNCTION protect_model_access_publication() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF ROW(NEW.deployment_id,NEW.binding_key,NEW.subject_id,NEW.operation_id,NEW.sequence,
      NEW.source_version,NEW.revision,NEW.content_hash,NEW.payload,NEW.issued_at,NEW.valid_until,NEW.created_at)
    IS DISTINCT FROM
    ROW(OLD.deployment_id,OLD.binding_key,OLD.subject_id,OLD.operation_id,OLD.sequence,
      OLD.source_version,OLD.revision,OLD.content_hash,OLD.payload,OLD.issued_at,OLD.valid_until,OLD.created_at) THEN
    RAISE EXCEPTION 'model access publication inputs are immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER model_access_publication_immutable BEFORE UPDATE ON model_access_publications
  FOR EACH ROW EXECUTE FUNCTION protect_model_access_publication();

CREATE FUNCTION protect_model_access_stream() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    IF EXISTS(SELECT 1 FROM deployments WHERE id=OLD.deployment_id) THEN
      RAISE EXCEPTION 'model access stream fences must be retained' USING ERRCODE='23514';
    END IF;
    RETURN OLD;
  END IF;
  IF ROW(NEW.deployment_id,NEW.binding_key,NEW.subject_id)
      IS DISTINCT FROM ROW(OLD.deployment_id,OLD.binding_key,OLD.subject_id)
    OR NEW.last_source_version < OLD.last_source_version OR NEW.last_sequence < OLD.last_sequence THEN
    RAISE EXCEPTION 'model access stream identity and versions cannot regress' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER model_access_stream_fence BEFORE UPDATE OR DELETE ON model_access_streams
  FOR EACH ROW EXECUTE FUNCTION protect_model_access_stream();

-- No automatic GC: pending delivery and deletion/replay fences are durable.
