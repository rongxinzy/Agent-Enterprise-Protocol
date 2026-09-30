-- Session client identity: a self-reported or User-Agent-derived label of the
-- software that created the session, so administrators can tell an Admin
-- Console browser login apart from a managed desktop client before revoking.
-- NULL columns mean no identity is known; the API renders them as
-- "client": null.
ALTER TABLE user_sessions
  ADD COLUMN client_name text,
  ADD COLUMN client_version text,
  ADD COLUMN client_device_id text;
