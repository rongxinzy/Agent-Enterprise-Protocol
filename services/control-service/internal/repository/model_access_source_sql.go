package repository

// One statement gives every component the same MVCC view even before the
// transactional signal migration is installed. The shared configuration lock
// additionally serializes the future producer with mutation-trigger fanout.
// Never SELECT u.*, license payload, token_hash, model endpoint or credentials.
const modelAccessSourceSQL = `
WITH context AS MATERIALIZED (
  SELECT ?::text AS deployment_id, ?::text AS user_id, statement_timestamp() AS observed_at
), principal AS (
  SELECT u.kind,u.status,u.require_password_change,p.user_id AS profile_id,
    CASE WHEN p.ephemeral THEN p.expires_at END AS expires_at
  FROM context c JOIN users u ON u.deployment_id=c.deployment_id AND u.id=c.user_id
  LEFT JOIN agent_profiles p ON p.deployment_id=u.deployment_id AND p.user_id=u.id
), effective_roles AS (
  SELECT b.role_id AS id FROM context c JOIN user_effective_roles b
    ON b.deployment_id=c.deployment_id AND b.user_id=c.user_id
), effective_teams AS (
  SELECT b.team_id AS id FROM context c JOIN user_team_bindings b
    ON b.deployment_id=c.deployment_id AND b.user_id=c.user_id
  JOIN teams t ON t.deployment_id=b.deployment_id AND t.id=b.team_id AND t.enabled
), role_models AS (
  SELECT m.id,CASE WHEN bool_or(a.expires_at IS NULL) THEN NULL ELSE max(a.expires_at) END AS expires_at
  FROM context c JOIN model_assignments a ON a.deployment_id=c.deployment_id AND a.subject_type='role'
  JOIN effective_roles r ON r.id=a.subject_id
  JOIN models m ON m.deployment_id=a.deployment_id AND m.id=a.model_id AND m.enabled
  WHERE a.expires_at IS NULL OR a.expires_at>c.observed_at GROUP BY m.id
), live_sessions AS (
  SELECT s.session_id AS id,max(t.expires_at) AS expires_at
  FROM context c JOIN user_sessions s ON s.deployment_id=c.deployment_id AND s.user_id=c.user_id
  JOIN user_session_tokens t ON t.session_id=s.session_id AND t.revoked_at IS NULL AND t.expires_at>c.observed_at
  WHERE s.revoked_at IS NULL GROUP BY s.session_id
), live_licenses AS (
  SELECT l.license_id AS id,l.digest,
    ARRAY(SELECT DISTINCT f FROM unnest(l.features) f ORDER BY f) AS features,
    CASE WHEN l.expires_at IS NULL THEN NULL ELSE l.grace_ends_at END AS expires_at
  FROM context c JOIN licenses l ON l.deployment_id=c.deployment_id AND l.status='active'
  WHERE l.expires_at IS NULL OR c.observed_at<=l.grace_ends_at
), live_workloads AS (
  SELECT w.* FROM context c JOIN model_access_workloads w
    ON w.deployment_id=c.deployment_id AND w.user_id=c.user_id
  WHERE w.retired_at IS NULL AND w.verified_until>c.observed_at
)
SELECT jsonb_build_object(
  'deploymentId',c.deployment_id,'subjectId',c.user_id,'observedAt',c.observed_at,
  'exists',EXISTS(SELECT 1 FROM principal),
  'kind',COALESCE((SELECT kind FROM principal),''),
  'disabled',NOT EXISTS(SELECT 1 FROM principal p WHERE p.status='active' AND NOT p.require_password_change
    AND (p.kind<>'agent' OR p.profile_id IS NOT NULL)
    AND (p.expires_at IS NULL OR p.expires_at>c.observed_at)),
  'profileMissing',EXISTS(SELECT 1 FROM principal WHERE kind='agent' AND profile_id IS NULL),
  'subjectExpiresAt',(SELECT expires_at FROM principal),
  'roleIds',COALESCE((SELECT jsonb_agg(id ORDER BY id) FROM effective_roles),'[]'::jsonb),
  'teamIds',COALESCE((SELECT jsonb_agg(id ORDER BY id) FROM effective_teams),'[]'::jsonb),
  'models',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'expiresAt',expires_at) ORDER BY id) FROM role_models),'[]'::jsonb),
  'sessions',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'expiresAt',expires_at) ORDER BY id) FROM live_sessions),'[]'::jsonb),
  'workloads',COALESCE((SELECT jsonb_agg(jsonb_build_object('employeeName',employee_name,'verifiedUntil',verified_until,
    'identity',jsonb_build_object('namespace',namespace,'employeeUid',employee_uid,'serviceAccountName',service_account_name,
      'serviceAccountUid',service_account_uid,'audience',audience)) ORDER BY binding_key) FROM live_workloads),'[]'::jsonb),
  'licenses',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'digest',digest,'features',features,'expiresAt',expires_at) ORDER BY id) FROM live_licenses),'[]'::jsonb),
  'legacyDirectGrants',EXISTS(
    SELECT 1 FROM model_assignments a JOIN models m ON m.deployment_id=a.deployment_id AND m.id=a.model_id AND m.enabled
    WHERE a.deployment_id=c.deployment_id AND (a.expires_at IS NULL OR a.expires_at>c.observed_at)
      AND ((a.subject_type='user' AND a.subject_id=c.user_id)
        OR (a.subject_type='team' AND a.subject_id IN (SELECT id FROM effective_teams)))
  )
) FROM context c`
