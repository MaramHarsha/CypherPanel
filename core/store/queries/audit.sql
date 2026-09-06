-- Audit log (audit-log.md §2, §4, §7). One immutable row per sensitive action.
-- There is no UPDATE here on purpose: the log is evidence, and evidence that
-- can be edited is not evidence. The only mutation is the retention purge.

-- InsertAuditEvent writes one row, resolving the ownership chain IN THE SAME
-- STATEMENT from whichever link the caller happened to know (§4). A handler
-- that knows only an environment gets project_id and team_id filled in; one
-- that knows only a project gets team_id; one that knows the team passes it
-- and nothing is looked up.
--
-- Resolving here rather than in Go is what makes the snapshot atomic with the
-- write: there is no window in which a project could move to another team
-- between the lookup and the insert, and a delete handler that passes team_id
-- explicitly still records the scope after the project row is gone.
-- name: InsertAuditEvent :one
INSERT INTO audit_events (
    id, action, outcome,
    actor_kind, actor_user_id, actor_token_id, actor_label,
    resource_kind, resource_id, resource_name,
    team_id, project_id, environment_id,
    detail, trace_id, client_ip
) VALUES (
    @id, @action, @outcome,
    @actor_kind, sqlc.narg('actor_user_id')::text, sqlc.narg('actor_token_id')::text, @actor_label,
    @resource_kind, @resource_id, @resource_name,
    COALESCE(
        sqlc.narg('team_id')::text,
        (SELECT p.team_id FROM projects p WHERE p.id = sqlc.narg('project_id')::text),
        (SELECT p.team_id FROM projects p
           JOIN environments e ON e.project_id = p.id
          WHERE e.id = sqlc.narg('environment_id')::text)
    ),
    COALESCE(
        sqlc.narg('project_id')::text,
        (SELECT e.project_id FROM environments e WHERE e.id = sqlc.narg('environment_id')::text)
    ),
    sqlc.narg('environment_id')::text,
    @detail::jsonb, @trace_id, @client_ip
)
RETURNING *;

-- name: GetAuditEvent :one
SELECT * FROM audit_events WHERE id = $1;

-- ListAuditEvents pages the log newest-first, with the caller's visibility
-- applied BEFORE any operator-supplied filter (§5). The `visible` CTE is the
-- whole tenancy story:
--
--   * a panel owner sees everything (@all_scopes) — the same superadmin bypass
--     teams.RoleInTeam already grants;
--   * anyone sees the rows of the teams they belong to (@team_ids);
--   * a panel admin additionally sees PANEL-scoped rows — a server, a user, the
--     mail/DNS/TLS settings — which belong to no team (@panel_scope);
--   * everyone sees their OWN actions, whatever scope they landed in, so
--     "what did I do?" never depends on still being in the team.
--
-- The cursor subquery reads from `visible`, not from the base table, so a
-- cursor naming an event the caller may not see yields an empty page instead of
-- restarting at the newest row — the same posture the inbox takes.
--
-- NOT MATERIALIZED is load-bearing, not decoration. `visible` is referenced
-- twice (the page and the cursor lookup), and from PostgreSQL 12 a
-- multiply-referenced CTE is materialized by default: the caller's ENTIRE
-- visible set would be built and spooled, then top-N sorted, before LIMIT — a
-- sequential scan and a temp-file sort on every page, growing with the whole
-- retention window, and cheap for any authenticated caller to loop. Inlining it
-- lets the planner push the predicate and the ordering into
-- idx_audit_events_at, which is what §2's index rationale promises. Measured on
-- 200k rows: CTE Scan + Sort 140 ms, Index Only Scan 0.14 ms.
-- name: ListAuditEvents :many
WITH visible AS NOT MATERIALIZED (
    SELECT e.* FROM audit_events e
    WHERE @all_scopes::boolean
       OR (e.team_id IS NOT NULL AND e.team_id = ANY(@team_ids::text[]))
       OR (e.team_id IS NULL AND @panel_scope::boolean)
       OR (e.actor_user_id IS NOT NULL AND e.actor_user_id = @viewer_id::text)
)
SELECT v.id, v.at, v.action, v.outcome,
       v.actor_kind, v.actor_user_id, v.actor_token_id, v.actor_label,
       v.resource_kind, v.resource_id, v.resource_name,
       v.team_id, v.project_id, v.environment_id,
       v.detail, v.trace_id, v.client_ip
FROM visible v
WHERE (@team_id::text = '' OR v.team_id = @team_id::text)
  AND (@project_id::text = '' OR v.project_id = @project_id::text)
  AND (@resource_id::text = '' OR v.resource_id = @resource_id::text)
  -- An action filter matches the exact verb or its whole family: `deploy`
  -- selects deploy.started, deploy.rolled_back and every later sibling, so a
  -- filter UI needs three coarse choices rather than the full vocabulary.
  AND (@action::text = '' OR v.action = @action::text OR v.action LIKE @action::text || '.%')
  -- An actor is named by id or by the label snapshot, so "priya@acme.com"
  -- works without first resolving it to a usr_… id.
  AND (@actor::text = '' OR v.actor_user_id = @actor::text OR v.actor_label = @actor::text)
  AND (@outcome::text = '' OR v.outcome = @outcome::text)
  AND (NOT @since_set::boolean OR v.at >= @since::timestamptz)
  AND (@before::text = '' OR (v.at, v.id) < (
        SELECT c.at, c.id FROM visible c WHERE c.id = @before::text
  ))
ORDER BY v.at DESC, v.id DESC
LIMIT @row_limit;

-- PurgeAuditEvents deletes one BOUNDED batch of rows older than the cutoff and
-- reports how many it removed, so the retention loop can drain a long backlog
-- in steps instead of taking one lock over the whole table (§8). The oldest
-- rows go first, which is also the order the index is already in.
-- name: PurgeAuditEvents :one
WITH doomed AS (
    SELECT e.id FROM audit_events e WHERE e.at < @cutoff ORDER BY e.at LIMIT @row_limit
), deleted AS (
    DELETE FROM audit_events a WHERE a.id IN (SELECT d.id FROM doomed d) RETURNING 1 AS one
)
SELECT count(*)::bigint FROM deleted;
