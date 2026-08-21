-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: UpdateUserProfile :one
UPDATE users
SET display_name = $2, timezone = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateUserEmail :one
UPDATE users
SET email = $2, updated_at = now()
WHERE id = $1
RETURNING *;
