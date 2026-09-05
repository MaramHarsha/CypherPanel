-- Deploy protection (deploy-protection.md §2, §4, §5): the protection document
-- per environment, its freeze windows, the one gate decision per deployment,
-- and the bounded break-glass grant that suspends a freeze.
--
-- Nothing here evaluates a window: minute-of-week arithmetic happens in
-- core/protection against an injected clock, because a window is wall clock in
-- its OWN zone and the database's now() knows nothing about that zone.

-- name: GetEnvironmentProtection :one
SELECT * FROM environment_protection WHERE environment_id = $1;

-- UpsertEnvironmentProtection replaces the whole document: PUT carries desired
-- state, so there is no partial-update path that could leave the flags and the
-- windows describing different intents (§6).
-- name: UpsertEnvironmentProtection :one
INSERT INTO environment_protection (
    environment_id, require_approval, min_approver_role, freeze_enabled
) VALUES ($1, $2, $3, $4)
ON CONFLICT (environment_id) DO UPDATE
SET require_approval  = EXCLUDED.require_approval,
    min_approver_role = EXCLUDED.min_approver_role,
    freeze_enabled    = EXCLUDED.freeze_enabled,
    updated_at        = now()
RETURNING *;

-- name: ListFreezeWindows :many
SELECT * FROM freeze_windows
WHERE environment_id = $1
ORDER BY start_dow, start_minute, id;

-- name: DeleteFreezeWindows :exec
DELETE FROM freeze_windows WHERE environment_id = $1;

-- InsertFreezeWindows writes a whole window list in one statement — the
-- replace half of the wholesale PUT. Positional unnests expand in lockstep
-- (PostgreSQL 10+), so column i of every array belongs to window i.
-- name: InsertFreezeWindows :exec
INSERT INTO freeze_windows (
    id, environment_id, start_dow, start_minute, end_dow, end_minute, timezone
)
SELECT unnest(@ids::text[]),
       @environment_id::text,
       unnest(@start_dows::smallint[]),
       unnest(@start_minutes::int[]),
       unnest(@end_dows::smallint[]),
       unnest(@end_minutes::int[]),
       unnest(@timezones::text[]);

-- ─── Approvals ──────────────────────────────────────────────────────────────

-- name: CreateDeployApproval :one
INSERT INTO deploy_approvals (
    deployment_id, environment_id, requested_by, required_role, state
) VALUES ($1, $2, $3, $4, 'pending')
RETURNING *;

-- GetDeployApproval joins the two user rows the pending card renders, so the
-- screen needs no second lookup to say "requested by alex@acme.com". A user
-- deleted since (ON DELETE SET NULL) leaves both id and email empty, which the
-- card reads as "pushed via webhook or by a since-removed account".
-- name: GetDeployApproval :one
SELECT a.*, COALESCE(rq.email, '') AS requested_by_email,
             COALESCE(dc.email, '') AS decided_by_email
FROM deploy_approvals a
LEFT JOIN users rq ON rq.id = a.requested_by
LEFT JOIN users dc ON dc.id = a.decided_by
WHERE a.deployment_id = $1;

-- ListDeployApprovalsByEnvironment is the environment's approval queue. An
-- empty @state means every state, so one query serves both the pending card and
-- the decision history (§6).
--
-- BOUNDED, like every other listing beside it: @state = '' asks for the whole
-- decision history of a long-lived environment, which is not a screen and is
-- not a response. Newest first, so the bound drops the oldest.
-- name: ListDeployApprovalsByEnvironment :many
SELECT a.*, COALESCE(rq.email, '') AS requested_by_email,
             COALESCE(dc.email, '') AS decided_by_email
FROM deploy_approvals a
LEFT JOIN users rq ON rq.id = a.requested_by
LEFT JOIN users dc ON dc.id = a.decided_by
WHERE a.environment_id = @environment_id
  AND (@state::text = '' OR a.state = @state::text)
ORDER BY a.created_at DESC
LIMIT @row_limit::int;

-- ListDeployApprovalsByApplication answers "which of THESE deployments has a
-- gate decision" for one page of a Deployments tab in one round trip, rather
-- than one lookup per row.
--
-- The deployment ids are the page the caller is decorating, not the
-- application's whole history: an application deploys forever, and a read model
-- that grows with it is a read model that eventually stops answering. The
-- application_id join stays as the tenancy assertion — an id from another
-- application matches nothing.
-- name: ListDeployApprovalsByApplication :many
SELECT a.*, COALESCE(rq.email, '') AS requested_by_email,
             COALESCE(dc.email, '') AS decided_by_email
FROM deploy_approvals a
JOIN deployments d ON d.id = a.deployment_id
LEFT JOIN users rq ON rq.id = a.requested_by
LEFT JOIN users dc ON dc.id = a.decided_by
WHERE d.application_id = @application_id
  AND a.deployment_id = ANY(@deployment_ids::text[]);

-- DecideDeployApproval is once-only by construction: the WHERE clause matches
-- nothing on an already-decided row, so a second approve (or an approve racing
-- a reject) writes nothing and the caller answers 409 (§5). No read-then-write,
-- so no window between the check and the update.
-- name: DecideDeployApproval :one
UPDATE deploy_approvals
SET state = @state, decided_by = @decided_by, decided_at = now(), reason = @reason
WHERE deployment_id = @deployment_id AND state = 'pending'
RETURNING *;

-- CountQualifiedApprovers counts the members of the project's team who rank at
-- or above @min_role, EXCLUDING @exclude_user_id. Zero means the excluded user
-- is the only qualifying approver, which is what lifts the no-self-approval
-- rule for a solo operator (§5). Panel owners are deliberately not counted:
-- their implicit rank is an authorization escape hatch, not a second person.
-- name: CountQualifiedApprovers :one
SELECT count(*) FROM projects p
JOIN team_members m ON m.team_id = p.team_id
WHERE p.id = @project_id
  AND m.user_id <> @exclude_user_id
  AND (CASE m.role WHEN 'owner' THEN 3 WHEN 'admin' THEN 2 WHEN 'member' THEN 1 ELSE 0 END)
      >= (CASE @min_role::text WHEN 'owner' THEN 3 WHEN 'admin' THEN 2 WHEN 'member' THEN 1 ELSE 0 END);

-- ─── Break glass ────────────────────────────────────────────────────────────

-- name: CreateBreakGlassGrant :one
INSERT INTO break_glass_grants (id, environment_id, opened_by, reason, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- CountActiveBreakGlassGrants asks the one question the freeze gate needs: is
-- an unexpired grant open right now. @now is the plane's injected clock, not
-- the database's, so the gate and its tests read the same time source.
-- name: CountActiveBreakGlassGrants :one
SELECT count(*) FROM break_glass_grants
WHERE environment_id = @environment_id AND expires_at > @now;

-- ListBreakGlassGrants shows who broke glass, why, and when it lapses —
-- newest first, bounded, because the screen shows "active + recent" and a
-- year of incidents is not a screen (§6).
-- name: ListBreakGlassGrants :many
SELECT g.*, COALESCE(u.email, '') AS opened_by_email
FROM break_glass_grants g
LEFT JOIN users u ON u.id = g.opened_by
WHERE g.environment_id = $1
ORDER BY g.created_at DESC
LIMIT $2;
