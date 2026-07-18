-- name: UpsertEnvVar :exec
INSERT INTO app_env_vars (application_id, key, value_ct, value_nonce)
VALUES ($1, $2, $3, $4)
ON CONFLICT (application_id, key)
DO UPDATE SET value_ct = EXCLUDED.value_ct, value_nonce = EXCLUDED.value_nonce;

-- name: ListEnvVars :many
SELECT * FROM app_env_vars WHERE application_id = $1 ORDER BY key;

-- name: DeleteEnvVar :exec
DELETE FROM app_env_vars WHERE application_id = $1 AND key = $2;
