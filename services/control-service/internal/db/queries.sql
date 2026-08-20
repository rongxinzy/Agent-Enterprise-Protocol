-- name: GetEnterprise :one
SELECT id, name, created_at FROM enterprises WHERE id = $1;

-- name: CreateEnterprise :one
INSERT INTO enterprises (id, name) VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name
RETURNING id, name, created_at;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE enterprise_id = $1 AND username = $2;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users WHERE enterprise_id = $1 AND ($2::text = '' OR id > $2)
ORDER BY id LIMIT $3;

-- name: CreateUser :one
INSERT INTO users (
  id, enterprise_id, username, display_name, email, password_hash,
  require_password_change, is_admin, organization_ids, role_ids
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
RETURNING *;

-- name: UpdateUser :one
UPDATE users SET
  display_name = COALESCE(sqlc.narg(display_name), display_name),
  email = COALESCE(sqlc.narg(email), email),
  status = COALESCE(sqlc.narg(status), status),
  organization_ids = COALESCE(sqlc.narg(organization_ids), organization_ids),
  role_ids = COALESCE(sqlc.narg(role_ids), role_ids),
  updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdatePassword :exec
UPDATE users SET password_hash = $2, require_password_change = $3, updated_at = now() WHERE id = $1;

-- name: GetAgent :one
SELECT * FROM agents WHERE agent_id = $1;

-- name: ListAgents :many
SELECT * FROM agents
WHERE enterprise_id = $1 AND ($2::text = '' OR agent_id > $2) AND ($3::text = '' OR user_id = $3)
ORDER BY agent_id LIMIT $4;

-- name: CreateAgent :one
INSERT INTO agents (agent_id, enterprise_id, user_id, agent_version, platform)
VALUES ($1,$2,$3,$4,$5)
RETURNING *;

-- name: TouchAgent :one
UPDATE agents SET agent_version = $2, platform = $3, last_seen_at = now()
WHERE agent_id = $1 RETURNING *;

-- name: CreateRefreshSession :exec
INSERT INTO refresh_sessions (token_hash, enterprise_id, user_id, agent_id, expires_at)
VALUES ($1,$2,$3,$4,$5);

-- name: GetRefreshSession :one
SELECT * FROM refresh_sessions WHERE token_hash = $1;

-- name: RevokeRefreshSession :exec
UPDATE refresh_sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :exec
UPDATE refresh_sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;
