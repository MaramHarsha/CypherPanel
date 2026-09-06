-- name: UpsertEnvVar :exec
INSERT INTO app_env_vars (application_id, key, value_ct, value_nonce, shared_refs)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (application_id, key)
DO UPDATE SET value_ct = EXCLUDED.value_ct,
              value_nonce = EXCLUDED.value_nonce,
              shared_refs = EXCLUDED.shared_refs;

-- name: ListEnvVars :many
SELECT * FROM app_env_vars WHERE application_id = $1 ORDER BY key;

-- name: DeleteEnvVar :exec
DELETE FROM app_env_vars WHERE application_id = $1 AND key = $2;

-- Keys and their shared-variable references, never the sealed value. This
-- exists so core/export's Store interface can be structurally incapable of
-- returning a ciphertext (project-export.md §4): ListEnvVars returns
-- value_ct/value_nonce, and an exporter that could call it would be one
-- serialization mistake away from a download containing every secret.
-- name: ListEnvVarKeys :many
SELECT key, shared_refs FROM app_env_vars WHERE application_id = $1 ORDER BY key;
