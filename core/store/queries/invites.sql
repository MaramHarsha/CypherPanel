-- Team invitations (invitations-and-access-requests.md §2, §4).
--
-- The wire token is `<id>.<secret>`: every lookup here is by the row's own id,
-- and only the HASH of the secret is stored, so the caller compares in constant
-- time before anything is spent. Nothing in this file selects by token_hash,
-- because nothing needs to — and a query that did would invite a plaintext
-- token to travel to the database.

-- name: CreateTeamInvite :one
INSERT INTO team_invites (
    id, team_id, email, role, token_hash, invited_by, invited_by_label, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetTeamInvite :one
SELECT * FROM team_invites WHERE id = $1;

-- ListTeamInvites serves the admin listing. include_decided=false is the
-- default view: only invites that can still be accepted, which is what an
-- operator means by "outstanding" (an expired one is not actionable, and it is
-- excluded here rather than filtered in Go so the page and the count agree).
-- name: ListTeamInvites :many
SELECT * FROM team_invites
WHERE team_id = @team_id
  AND (@include_decided::boolean
       OR (accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()))
ORDER BY created_at DESC;

-- RevokeTeamInvite is idempotent by predicate: no row back means it was
-- already revoked, already accepted, or never existed — one undifferentiated
-- answer, which is the same discipline the accept path uses.
-- name: RevokeTeamInvite :one
UPDATE team_invites
SET revoked_at = now()
WHERE id = @id AND team_id = @team_id AND accepted_at IS NULL AND revoked_at IS NULL
RETURNING *;

-- RevokeLiveTeamInvitesFor supersedes: re-inviting an address kills whatever is
-- outstanding for it on this team, so the partial unique index can never be
-- violated by an ordinary re-invite and an operator is never stuck behind a
-- link they cannot see (§2).
-- name: RevokeLiveTeamInvitesFor :execrows
UPDATE team_invites
SET revoked_at = now()
WHERE team_id = @team_id AND email = @email
  AND accepted_at IS NULL AND revoked_at IS NULL;

-- RevokeLiveTeamInvitesBy revokes the invitations one member issued for one
-- team. Called when that member is removed: an invite outlives its issuer's
-- session, but not their membership — otherwise a removed admin keeps a 7-day
-- back door in an envelope (§8).
-- name: RevokeLiveTeamInvitesBy :execrows
UPDATE team_invites
SET revoked_at = now()
WHERE team_id = @team_id AND invited_by = @invited_by
  AND accepted_at IS NULL AND revoked_at IS NULL;

-- AcceptTeamInvite is the spend, and it is ONE statement on purpose: no row
-- back means expired, revoked, or already accepted by whoever got there first.
-- A read-then-write would grant the same invite twice under a double-submit.
-- name: AcceptTeamInvite :one
UPDATE team_invites
SET accepted_at = now()
WHERE id = @id AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
RETURNING *;
