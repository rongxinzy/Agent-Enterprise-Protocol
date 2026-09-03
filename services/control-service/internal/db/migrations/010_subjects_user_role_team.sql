-- Convert legacy resource grants before restricting assignment subjects.
INSERT INTO teams (deployment_id, id, name, description)
SELECT o.enterprise_id, o.id, o.name, 'Migrated from organization'
FROM organizations o
ON CONFLICT (deployment_id, id) DO NOTHING;

INSERT INTO user_team_bindings (deployment_id, user_id, team_id, is_primary)
SELECT u.enterprise_id, u.id, o.id, false
FROM users u
JOIN organizations o ON o.enterprise_id=u.enterprise_id AND o.id=ANY(u.organization_ids)
ON CONFLICT DO NOTHING;

-- Enterprise grants become the built-in all-users Team.
INSERT INTO skill_assignments (id, enterprise_id, skill_id, subject_type, subject_id)
SELECT md5(sa.id || ':all-users')::text, sa.enterprise_id, sa.skill_id, 'team', 'all-users'
FROM skill_assignments sa
WHERE sa.subject_type='enterprise'
ON CONFLICT (enterprise_id, skill_id, subject_type, subject_id) DO NOTHING;
INSERT INTO model_assignments (id, enterprise_id, model_id, subject_type, subject_id)
SELECT md5(ma.id || ':all-users')::text, ma.enterprise_id, ma.model_id, 'team', 'all-users'
FROM model_assignments ma
WHERE ma.subject_type='enterprise'
ON CONFLICT (enterprise_id, model_id, subject_type, subject_id) DO NOTHING;
INSERT INTO credential_assignments (id, enterprise_id, credential_id, subject_type, subject_id)
SELECT md5(ca.id || ':all-users')::text, ca.enterprise_id, ca.credential_id, 'team', 'all-users'
FROM credential_assignments ca
WHERE ca.subject_type='enterprise'
ON CONFLICT (enterprise_id, credential_id, subject_type, subject_id) DO NOTHING;

-- Agent grants become user grants through the existing binding.
INSERT INTO skill_assignments (id, enterprise_id, skill_id, subject_type, subject_id)
SELECT md5(sa.id || ':user')::text, sa.enterprise_id, sa.skill_id, 'user', a.user_id
FROM skill_assignments sa JOIN agents a ON a.agent_id=sa.subject_id
WHERE sa.subject_type='agent'
ON CONFLICT (enterprise_id, skill_id, subject_type, subject_id) DO NOTHING;
INSERT INTO model_assignments (id, enterprise_id, model_id, subject_type, subject_id)
SELECT md5(ma.id || ':user')::text, ma.enterprise_id, ma.model_id, 'user', a.user_id
FROM model_assignments ma JOIN agents a ON a.agent_id=ma.subject_id
WHERE ma.subject_type='agent'
ON CONFLICT (enterprise_id, model_id, subject_type, subject_id) DO NOTHING;
INSERT INTO credential_assignments (id, enterprise_id, credential_id, subject_type, subject_id)
SELECT md5(ca.id || ':user')::text, ca.enterprise_id, ca.credential_id, 'user', a.user_id
FROM credential_assignments ca JOIN agents a ON a.agent_id=ca.subject_id
WHERE ca.subject_type='agent'
ON CONFLICT (enterprise_id, credential_id, subject_type, subject_id) DO NOTHING;

-- Organization grants map to the migrated Team with the same ID.
INSERT INTO skill_assignments (id, enterprise_id, skill_id, subject_type, subject_id)
SELECT md5(sa.id || ':organization')::text, sa.enterprise_id, sa.skill_id, 'team', sa.subject_id
FROM skill_assignments sa
WHERE sa.subject_type='organization'
ON CONFLICT (enterprise_id, skill_id, subject_type, subject_id) DO NOTHING;
INSERT INTO model_assignments (id, enterprise_id, model_id, subject_type, subject_id)
SELECT md5(ma.id || ':organization')::text, ma.enterprise_id, ma.model_id, 'team', ma.subject_id
FROM model_assignments ma
WHERE ma.subject_type='organization'
ON CONFLICT (enterprise_id, model_id, subject_type, subject_id) DO NOTHING;
INSERT INTO credential_assignments (id, enterprise_id, credential_id, subject_type, subject_id)
SELECT md5(ca.id || ':organization')::text, ca.enterprise_id, ca.credential_id, 'team', ca.subject_id
FROM credential_assignments ca
WHERE ca.subject_type='organization'
ON CONFLICT (enterprise_id, credential_id, subject_type, subject_id) DO NOTHING;

DELETE FROM skill_assignments WHERE subject_type IN ('enterprise','organization','agent');
DELETE FROM model_assignments WHERE subject_type IN ('enterprise','organization','agent');
DELETE FROM credential_assignments WHERE subject_type IN ('enterprise','organization','agent');

ALTER TABLE skill_assignments DROP CONSTRAINT IF EXISTS skill_assignments_subject_type_check;
ALTER TABLE skill_assignments ADD CONSTRAINT skill_assignments_subject_type_check CHECK (subject_type IN ('user', 'role', 'team'));
ALTER TABLE model_assignments DROP CONSTRAINT IF EXISTS model_assignments_subject_type_check;
ALTER TABLE model_assignments ADD CONSTRAINT model_assignments_subject_type_check CHECK (subject_type IN ('user', 'role', 'team'));
ALTER TABLE credential_assignments DROP CONSTRAINT IF EXISTS credential_assignments_subject_type_check;
ALTER TABLE credential_assignments ADD CONSTRAINT credential_assignments_subject_type_check CHECK (subject_type IN ('user', 'role', 'team'));
