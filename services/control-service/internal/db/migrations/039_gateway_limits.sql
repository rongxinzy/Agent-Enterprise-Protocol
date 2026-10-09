-- Configuration/publication only. Native Higress/Redis own all counters.
CREATE TABLE gateway_limits (
  deployment_id text NOT NULL REFERENCES deployments(id),
  id text NOT NULL,
  version bigint NOT NULL CHECK (version > 0),
  configuration jsonb NOT NULL,
  deleted boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id,id)
);
CREATE TABLE gateway_limit_publications (
  deployment_id text PRIMARY KEY REFERENCES deployments(id),
  revision text NOT NULL,
  items jsonb NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','applied','error')),
  observed_revision text,
  applied_at timestamptz
);
