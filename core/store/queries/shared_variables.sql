-- Project shared variables (shared-variables.md §2, §5, §7).
--
-- Two ideas recur below and are worth reading once:
--
--   SHADOWING — a key resolves to the row scoped to the application's own
--   environment when one exists, otherwise the project-scoped row. In SQL that
--   is `DISTINCT ON (key) … ORDER BY key, (environment_id IS NOT NULL) DESC`:
--   TRUE sorts before FALSE, so the environment row wins.
--
--   SCOPE-ACCURATE USAGE — an application whose own environment defines a
--   shadowing row of the same key does NOT use the project-scoped variable, so
--   it is excluded from that variable's count and listing (§7).

-- name: CreateSharedVariable :one
INSERT INTO shared_variables (
    id, project_id, environment_id, key, value_ct, value_nonce
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetSharedVariable :one
SELECT * FROM shared_variables WHERE id = $1;

-- name: ListSharedVariablesByProject :many
SELECT * FROM shared_variables
WHERE project_id = $1
ORDER BY key, environment_id NULLS FIRST;

-- UpdateSharedVariableValue is the ONLY mutation: key and environment_id are
-- immutable after create, because changing either would silently re-point or
-- orphan every referencing application (§7). updated_at always moves, even when
-- the plaintext is unchanged — AES-GCM with a fresh nonce yields different
-- ciphertext for identical plaintext, so comparing would mean unsealing on every
-- write, and a redeploy marker that turns out to be a no-op is safe while a
-- missed one is not (§5).
-- name: UpdateSharedVariableValue :one
UPDATE shared_variables
SET value_ct = $2, value_nonce = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteSharedVariable :exec
DELETE FROM shared_variables WHERE id = $1;

-- ListSharedVariablesInScope resolves the variables in force for one
-- environment: project-scoped rows, with environment-scoped rows of the same key
-- shadowing them. This is the scheduler's single resolution point (§4).
-- name: ListSharedVariablesInScope :many
SELECT DISTINCT ON (key) *
FROM shared_variables
WHERE project_id = $1
  AND (environment_id IS NULL OR environment_id = $2)
ORDER BY key, (environment_id IS NOT NULL) DESC;

-- ListSharedVariableKeysInScope is the same resolution reduced to key names —
-- what the write-time reference check reads (§3). Keys only: nothing unseals.
-- name: ListSharedVariableKeysInScope :many
SELECT DISTINCT key
FROM shared_variables
WHERE project_id = $1
  AND (environment_id IS NULL OR environment_id = $2)
ORDER BY key;

-- name: CountSharedVariableUsage :one
SELECT COUNT(*)::bigint AS used_by_count
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN shared_variables sv ON sv.id = $1
WHERE e.project_id = sv.project_id
  AND (sv.environment_id IS NULL OR e.id = sv.environment_id)
  AND EXISTS (
      SELECT 1 FROM app_env_vars v
      WHERE v.application_id = a.id AND sv.key = ANY (v.shared_refs)
  )
  AND (sv.environment_id IS NOT NULL OR NOT EXISTS (
      SELECT 1 FROM shared_variables sh
      WHERE sh.project_id = sv.project_id
        AND sh.environment_id = e.id
        AND sh.key = sv.key
  ));

-- name: CountSharedVariableUsageByProject :many
SELECT sv.id, (
    SELECT COUNT(*)
    FROM applications a
    JOIN environments e ON e.id = a.environment_id
    WHERE e.project_id = sv.project_id
      AND (sv.environment_id IS NULL OR e.id = sv.environment_id)
      AND EXISTS (
          SELECT 1 FROM app_env_vars v
          WHERE v.application_id = a.id AND sv.key = ANY (v.shared_refs)
      )
      AND (sv.environment_id IS NOT NULL OR NOT EXISTS (
          SELECT 1 FROM shared_variables sh
          WHERE sh.project_id = sv.project_id
            AND sh.environment_id = e.id
            AND sh.key = sv.key
      ))
)::bigint AS used_by_count
FROM shared_variables sv
WHERE sv.project_id = $1;

-- ListSharedVariableUsage names the applications that reference one variable,
-- each with its own drift marker relative to THIS variable (§7). A NULL
-- env_applied_at means the application has never had a resolved environment
-- observed running, so the current value has never been applied — pending.
-- name: ListSharedVariableUsage :many
SELECT a.id            AS application_id,
       a.name          AS application_name,
       e.name          AS environment_name,
       (a.env_applied_at IS NULL OR sv.updated_at > a.env_applied_at) AS redeploy_pending
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN shared_variables sv ON sv.id = $1
WHERE e.project_id = sv.project_id
  AND (sv.environment_id IS NULL OR e.id = sv.environment_id)
  AND EXISTS (
      SELECT 1 FROM app_env_vars v
      WHERE v.application_id = a.id AND sv.key = ANY (v.shared_refs)
  )
  AND (sv.environment_id IS NOT NULL OR NOT EXISTS (
      SELECT 1 FROM shared_variables sh
      WHERE sh.project_id = sv.project_id
        AND sh.environment_id = e.id
        AND sh.key = sv.key
  ))
ORDER BY e.name, a.name;

-- ApplicationRedeployPending is the drift marker for one application (§5):
-- some shared variable in its scope, named in one of its shared_refs, changed
-- after the environment it is actually running was frozen onto the wire.
-- name: ApplicationRedeployPending :one
SELECT EXISTS (
    SELECT 1
    FROM applications a
    JOIN environments e ON e.id = a.environment_id
    JOIN app_env_vars v ON v.application_id = a.id
    JOIN shared_variables sv
      ON sv.project_id = e.project_id
     AND (sv.environment_id IS NULL OR sv.environment_id = e.id)
     AND sv.key = ANY (v.shared_refs)
    WHERE a.id = $1
      AND (a.env_applied_at IS NULL OR sv.updated_at > a.env_applied_at)
      AND (sv.environment_id IS NOT NULL OR NOT EXISTS (
          SELECT 1 FROM shared_variables sh
          WHERE sh.project_id = sv.project_id
            AND sh.environment_id = e.id
            AND sh.key = sv.key
      ))
) AS redeploy_pending;

-- ListRedeployPendingApplications answers the same question for a whole
-- environment in one query, so listing applications stays a fixed number of
-- round trips rather than one per row.
-- name: ListRedeployPendingApplications :many
SELECT DISTINCT a.id
FROM applications a
JOIN environments e ON e.id = a.environment_id
JOIN app_env_vars v ON v.application_id = a.id
JOIN shared_variables sv
  ON sv.project_id = e.project_id
 AND (sv.environment_id IS NULL OR sv.environment_id = e.id)
 AND sv.key = ANY (v.shared_refs)
WHERE a.environment_id = $1
  AND (a.env_applied_at IS NULL OR sv.updated_at > a.env_applied_at)
  AND (sv.environment_id IS NOT NULL OR NOT EXISTS (
      SELECT 1 FROM shared_variables sh
      WHERE sh.project_id = sv.project_id
        AND sh.environment_id = e.id
        AND sh.key = sv.key
  ));

-- SetDeploymentEnvResolved stamps the instant buildSpec froze this rollout's
-- environment onto the wire (§5). The stamp is the DATABASE's now(), not the
-- caller's clock, because it is only ever compared against
-- shared_variables.updated_at — also a database now(). Reading two clocks would
-- make the drift marker wrong by exactly the skew between the plane and
-- Postgres, in whichever direction happened to hide a needed redeploy.
-- name: SetDeploymentEnvResolved :exec
UPDATE deployments SET env_resolved_at = now() WHERE id = $1;

-- ApplyDeploymentEnvStamp copies a deployment's env_resolved_at onto its
-- application, and is called only when that deployment is OBSERVED running
-- (§5) — which is what makes a failed deploy unable to mark an app clean.
-- name: ApplyDeploymentEnvStamp :exec
UPDATE applications a
SET env_applied_at = d.env_resolved_at
FROM deployments d
WHERE d.id = $1
  AND a.id = d.application_id
  AND d.env_resolved_at IS NOT NULL;

-- Keys and scope, never the sealed value — so core/export's Store interface
-- can be structurally incapable of holding a ciphertext (project-export.md §4).
-- name: ListSharedVariableKeysByProject :many
SELECT key, environment_id FROM shared_variables
WHERE project_id = $1
ORDER BY key, environment_id NULLS FIRST;
