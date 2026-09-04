-- name: GetDeployment :one
SELECT id, name, created_at FROM deployments WHERE id = $1;

-- name: CreateDeployment :one
INSERT INTO deployments (id, name) VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name
RETURNING id, name, created_at;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE deployment_id = $1 AND username = $2;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users WHERE deployment_id = $1 AND ($2::text = '' OR id > $2)
ORDER BY id LIMIT $3;

-- name: CreateUser :one
INSERT INTO users (
  id, deployment_id, username, display_name, email, password_hash,
  require_password_change, is_admin
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING *;

-- name: UpdateUser :one
UPDATE users SET
  display_name = COALESCE(sqlc.narg(display_name), display_name),
  email = COALESCE(sqlc.narg(email), email),
  status = COALESCE(sqlc.narg(status), status),
  updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdatePassword :exec
UPDATE users SET password_hash = $2, require_password_change = $3, updated_at = now() WHERE id = $1;

-- User sessions are managed transactionally by the application because token
-- rotation also updates the session cursor and topic.
