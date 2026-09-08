-- +goose Up
-- V1.x, invitations-and-access-requests.md §2: the two ways a person gets into
-- a team from outside the panel's existing "an admin adds an account that
-- already exists" path — an Invitation (a bearer link that grants one
-- membership once) and an Access Request (a member asking the owners for a
-- higher rank).
--
-- Both are the shape join_tokens and email_changes already use: a row that
-- describes a future change, spent by a single atomic UPDATE. Only the HASH of
-- an invitation's secret is stored (ENGINEERING rules 20–22).
--
-- Additive and reversible (rule 16): two new tables plus one nullable column on
-- inbox_items.

CREATE TABLE team_invites (
    id      TEXT NOT NULL PRIMARY KEY,                        -- inv_… prefix
    team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    -- The address as the panel parsed and lower-cased it, never as it was
    -- typed: it reaches a recipient list and an email body (CWE-640).
    email TEXT NOT NULL,
    role  TEXT NOT NULL,                                      -- member | admin | owner
    -- sha256 of the wire secret. The wire token is `<id>.<secret>`, so a lookup
    -- is an indexed primary-key read and the secret is compared in constant
    -- time before anything is spent — a wrong guess against a real id can
    -- therefore never burn a valid invite.
    token_hash BYTEA NOT NULL,
    -- SET NULL, beside a snapshot: the accept landing says "sam@… invited you",
    -- and that sentence must survive Sam's account being deleted (§2).
    invited_by       TEXT REFERENCES users(id) ON DELETE SET NULL,
    invited_by_label TEXT NOT NULL DEFAULT '',
    expires_at  TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One LIVE invite per (team, address). Re-inviting supersedes rather than
-- conflicting: the create path revokes the outstanding one first, so an
-- operator is never stuck behind a link they cannot see (§2).
CREATE UNIQUE INDEX idx_team_invites_live ON team_invites (team_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX idx_team_invites_team ON team_invites (team_id, created_at DESC);

CREATE TABLE access_requests (
    id      TEXT NOT NULL PRIMARY KEY,                        -- acr_… prefix
    team_id TEXT NOT NULL REFERENCES teams(id)  ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    requested_role TEXT NOT NULL,                             -- member | admin | owner
    message        TEXT NOT NULL DEFAULT '',                  -- ≤ 500 chars, bounded in Go
    state          TEXT NOT NULL DEFAULT 'pending',           -- pending | granted | denied
    -- Same snapshot discipline as invited_by: the owner who decided may be
    -- deleted, and "granted by sam@…" must still read.
    decided_by       TEXT REFERENCES users(id) ON DELETE SET NULL,
    decided_by_label TEXT NOT NULL DEFAULT '',
    decision_reason  TEXT NOT NULL DEFAULT '',
    decided_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One OPEN request per (team, user): the owners' inbox must not fill with the
-- same ask. A decided request is history and does not block a later one.
CREATE UNIQUE INDEX idx_access_requests_open ON access_requests (team_id, user_id)
    WHERE state = 'pending';
CREATE INDEX idx_access_requests_team ON access_requests (team_id, created_at DESC);

-- The inbox gains its first TEAM-scoped items (invitations-and-access-
-- requests.md §6): access.requested/granted/denied and invite.accepted belong
-- to a team, not to a project and not to the panel. Nullable exactly like
-- project_id (migration 0028): a project-scoped write sets project_id and
-- leaves this NULL, a team-scoped one does the reverse, and a panel-level one
-- sets neither.
ALTER TABLE inbox_items ADD COLUMN team_id TEXT REFERENCES teams(id) ON DELETE CASCADE;

-- +goose Down
-- The team-scoped items go with the column that scopes them: a row whose kind
-- no longer exists and whose audience can no longer be reasoned about is worse
-- than no row (the same reasoning as migration 0028's down step).
DELETE FROM inbox_items WHERE team_id IS NOT NULL;
ALTER TABLE inbox_items DROP COLUMN team_id;
DROP TABLE IF EXISTS access_requests;
DROP TABLE IF EXISTS team_invites;
