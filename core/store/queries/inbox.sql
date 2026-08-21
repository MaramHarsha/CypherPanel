-- Notification inbox (notification-inbox.md §4, §6): recipient resolution, the
-- fan-out insert, the digest roll-up, the seek page, the unread count, the two
-- mark verbs, preferences, and the two deletes that keep the rule "never hold
-- an item for a team you do not belong to".

-- ListInboxRecipients resolves an observed outcome to the users who should
-- receive it: the members of the team that owns the project, minus anyone who
-- has muted this kind (§4 step 1). Membership is the subscription — a panel
-- owner's implicit team-owner rank is an authorization escape hatch, not an
-- opinion about what they want to read, so it is deliberately not consulted
-- here. An absent preferences row yields an empty array through COALESCE, so it
-- receives everything.
-- name: ListInboxRecipients :many
SELECT m.user_id
FROM projects p
JOIN team_members m ON m.team_id = p.team_id
LEFT JOIN inbox_preferences pr ON pr.user_id = m.user_id
WHERE p.id = @project_id
  AND NOT (@kind::text = ANY(COALESCE(pr.muted_kinds, '{}'::text[])))
ORDER BY m.user_id;

-- InsertInboxItems writes one immediate item per recipient in a single
-- statement: the shared columns are scalars, the per-recipient id/user pairs
-- arrive through unnest. ON CONFLICT DO NOTHING is the redelivery guard — the
-- same terminal observation handled twice leaves the row untouched
-- (ENGINEERING rule 12).
-- name: InsertInboxItems :exec
WITH recipients AS (
    -- Two single-argument unnests in one select list expand in lockstep
    -- (PostgreSQL 10+), which pairs each minted id with its recipient. Written
    -- this way rather than as unnest(a, b) because that two-argument form is
    -- only valid in FROM, and the generator's catalogue does not carry it.
    SELECT unnest(@ids::text[]) AS id, unnest(@user_ids::text[]) AS user_id
)
INSERT INTO inbox_items (
    id, user_id, project_id, kind, severity, digest,
    title, body, link, link_label, count_ok, count_total, sources, dedupe_key
)
SELECT r.id, r.user_id, @project_id::text, @kind::text, @severity::text, false,
       @title::text, @body::text, @link::text, @link_label::text,
       1, 1, '{}'::text[], @dedupe_key::text
FROM recipients r
ON CONFLICT (user_id, dedupe_key) DO NOTHING;

-- UpsertInboxDigests is the success path: the first event in a (user, project,
-- kind, UTC day) window creates the digest row, every later one increments it.
-- A digest is one unread however many events it holds — that is the whole point
-- of digesting. The sources guard makes a redelivered event a no-op rather than
-- a silently inflated counter (§4).
-- name: UpsertInboxDigests :exec
WITH recipients AS (
    SELECT unnest(@ids::text[]) AS id, unnest(@user_ids::text[]) AS user_id
)
INSERT INTO inbox_items (
    id, user_id, project_id, kind, severity, digest,
    title, body, link, link_label, count_ok, count_total, sources, dedupe_key
)
SELECT r.id, r.user_id, @project_id::text, @kind::text, @severity::text, true,
       @title::text, '', '', '', 1, 1, ARRAY[@focus_id::text], @dedupe_key::text
FROM recipients r
ON CONFLICT (user_id, dedupe_key) DO UPDATE
SET count_ok    = inbox_items.count_ok + 1,
    count_total = inbox_items.count_total + 1,
    sources     = inbox_items.sources || @focus_id::text,
    updated_at  = now()
WHERE NOT (@focus_id::text = ANY(inbox_items.sources));

-- BumpInboxDigestTotals is the failure path's half of the denominator: a
-- failure in the same window bumps count_total ONLY, and never creates a digest
-- (§3). That is what makes "Backups: 2/3 succeeded" honest, sitting beside the
-- one immediate failure item that explains the missing third.
-- The dedupe key already carries the kind, the project and the UTC day, so it
-- selects exactly the digests of that window and nothing else — no recipient
-- list is needed, and none is resolved. A user who left the team has no rows
-- left to bump.
-- name: BumpInboxDigestTotals :exec
UPDATE inbox_items
SET count_total = count_total + 1,
    sources     = sources || @focus_id::text,
    updated_at  = now()
WHERE dedupe_key = @dedupe_key::text
  AND digest = true
  AND NOT (@focus_id::text = ANY(sources));

-- ListInboxItems is the first seek page, newest first. unread_only is a plain
-- boolean rather than two queries so the filter and the page order can never
-- drift apart.
-- name: ListInboxItems :many
SELECT * FROM inbox_items
WHERE user_id = @user_id
  AND (NOT @unread_only::boolean OR read_at IS NULL)
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;

-- ListInboxItemsBefore continues from the cursor row on (created_at, id) DESC.
-- The cursor row is itself scoped to the caller, so a cursor naming someone
-- else's item makes the comparison NULL and the page comes back empty rather
-- than restarting at the newest row.
-- name: ListInboxItemsBefore :many
SELECT i.* FROM inbox_items i
WHERE i.user_id = @user_id
  AND (NOT @unread_only::boolean OR i.read_at IS NULL)
  AND (i.created_at, i.id) < (
      SELECT c.created_at, c.id FROM inbox_items c
      WHERE c.id = @before AND c.user_id = @user_id
  )
ORDER BY i.created_at DESC, i.id DESC
LIMIT @row_limit;

-- name: CountUnreadInboxItems :one
SELECT count(*) FROM inbox_items WHERE user_id = $1 AND read_at IS NULL;

-- MarkInboxItemRead is idempotent: COALESCE keeps the original read time, so
-- marking an already-read item changes nothing and still returns the row. No
-- rows means the item does not exist OR is not the caller's — the same answer
-- either way, which is the point (§5).
-- name: MarkInboxItemRead :one
UPDATE inbox_items
SET read_at = COALESCE(read_at, now()), updated_at = now()
WHERE id = @id AND user_id = @user_id
RETURNING *;

-- name: MarkAllInboxItemsRead :one
WITH marked AS (
    UPDATE inbox_items SET read_at = now(), updated_at = now()
    WHERE user_id = $1 AND read_at IS NULL
    RETURNING 1 AS one
)
SELECT count(*) FROM marked;

-- PruneInboxItems trims every affected user's inbox to the most recent
-- keep_rows in ONE statement (§4 step 4, §5's "one prune per event"): ranking
-- inside a single partitioned scan, rather than the correlated NOT IN the
-- single-owner logs use, is what keeps a twenty-member fan-out to one query
-- instead of twenty.
-- name: PruneInboxItems :exec
DELETE FROM inbox_items
WHERE id IN (
    SELECT ranked.id FROM (
        SELECT i.id,
               row_number() OVER (PARTITION BY i.user_id ORDER BY i.created_at DESC, i.id DESC) AS rn
        FROM inbox_items i
        WHERE i.user_id = ANY(@user_ids::text[])
    ) ranked
    WHERE ranked.rn > @keep_rows::bigint
);

-- DeleteInboxItemsForTeamMember empties a team's items from an ex-member's
-- inbox (§4). The rule is "never hold an item for a team you do not belong to",
-- and a stale title naming a project you were just removed from breaks it as
-- surely as a live delivery would.
-- name: DeleteInboxItemsForTeamMember :exec
DELETE FROM inbox_items
WHERE user_id = @user_id
  AND project_id IN (SELECT id FROM projects WHERE team_id = @team_id);

-- name: GetInboxPreferences :one
SELECT * FROM inbox_preferences WHERE user_id = $1;

-- name: UpsertInboxPreferences :one
INSERT INTO inbox_preferences (user_id, muted_kinds) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE
SET muted_kinds = EXCLUDED.muted_kinds, updated_at = now()
RETURNING *;
