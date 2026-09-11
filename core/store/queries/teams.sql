-- name: CreateTeam :one
INSERT INTO teams (id, name) VALUES ($1, $2) RETURNING *;

-- name: GetTeam :one
SELECT * FROM teams WHERE id = $1;

-- name: ListTeams :many
SELECT * FROM teams ORDER BY created_at;

-- name: RenameTeam :one
UPDATE teams SET name = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeleteTeam :exec
DELETE FROM teams WHERE id = $1;

-- name: CountTeamProjects :one
SELECT count(*) FROM projects WHERE team_id = $1;

-- name: UpsertTeamMember :one
INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)
ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role
RETURNING *;

-- name: GetTeamMember :one
SELECT * FROM team_members WHERE team_id = $1 AND user_id = $2;

-- name: ListTeamMembers :many
SELECT m.team_id, m.user_id, u.email, m.role, m.created_at
FROM team_members m JOIN users u ON u.id = m.user_id
WHERE m.team_id = $1 ORDER BY m.created_at;

-- name: DeleteTeamMember :exec
DELETE FROM team_members WHERE team_id = $1 AND user_id = $2;

-- name: CountTeamOwners :one
SELECT count(*) FROM team_members WHERE team_id = $1 AND role = 'owner';

-- name: ListTeamsByUser :many
SELECT t.id, t.name, t.created_at, t.updated_at, m.role
FROM teams t JOIN team_members m ON m.team_id = t.id
WHERE m.user_id = $1 ORDER BY t.created_at;

-- name: GetTeamRoleForProject :one
SELECT m.role FROM projects p
JOIN team_members m ON m.team_id = p.team_id AND m.user_id = $2
WHERE p.id = $1;

-- name: ListProjectsByUser :many
SELECT p.* FROM projects p
JOIN team_members m ON m.team_id = p.team_id
WHERE m.user_id = $1 ORDER BY p.created_at DESC;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at;

-- name: UpdateUserRole :one
UPDATE users SET role = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;
