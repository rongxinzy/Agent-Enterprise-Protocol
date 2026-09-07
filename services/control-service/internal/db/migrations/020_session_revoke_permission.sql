-- Session inspection and session revocation are separate administrative
-- capabilities. Existing administrators receive the new permission through
-- the normal bootstrap permission sync; custom roles must opt in explicitly.
INSERT INTO permissions (id, description)
VALUES ('sessions.write', 'Revoke individual user sessions')
ON CONFLICT (id) DO NOTHING;
