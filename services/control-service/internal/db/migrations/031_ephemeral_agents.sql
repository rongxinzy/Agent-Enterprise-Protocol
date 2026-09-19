-- Ephemeral conversation-scoped digital employees: a fork of a department
-- agent persona carrying a frozen snapshot of one requester's data scope.
ALTER TABLE agent_profiles
  ADD COLUMN ephemeral boolean NOT NULL DEFAULT false,
  ADD COLUMN expires_at timestamptz NULL,
  ADD CONSTRAINT agent_profiles_ephemeral_expiry_ck
    CHECK ((ephemeral AND expires_at IS NOT NULL) OR (NOT ephemeral AND expires_at IS NULL));

CREATE INDEX agent_profiles_ephemeral_idx ON agent_profiles (deployment_id, ephemeral);

-- Delegation-chain authority: one digital employee invoking another.
INSERT INTO permissions (id, description) VALUES
  ('agents.invoke', 'Invoke another digital employee on behalf of a requester')
ON CONFLICT (id) DO NOTHING;
