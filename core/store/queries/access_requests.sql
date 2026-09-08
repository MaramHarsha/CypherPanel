-- Access requests (invitations-and-access-requests.md §2, §5): a member asking
-- a team's owners for a higher rank. No secret, no bearer token — the row IS
-- the ask, and the two decision verbs are the only mutations.

-- name: CreateAccessRequest :one
INSERT INTO access_requests (id, team_id, user_id, requested_role, message)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- GetAccessRequest also carries the requester's address and their CURRENT role
-- in the team, both derived at read time: the owner's card says
-- "member → admin" without a second call, and it says it about the membership
-- as it stands now rather than as it stood when the ask was made.
-- name: GetAccessRequest :one
SELECT r.*, u.email AS user_email, COALESCE(m.role, '') AS current_role
FROM access_requests r
JOIN users u ON u.id = r.user_id
LEFT JOIN team_members m ON m.team_id = r.team_id AND m.user_id = r.user_id
WHERE r.id = $1;

-- name: ListAccessRequests :many
SELECT r.*, u.email AS user_email, COALESCE(m.role, '') AS current_role
FROM access_requests r
JOIN users u ON u.id = r.user_id
LEFT JOIN team_members m ON m.team_id = r.team_id AND m.user_id = r.user_id
WHERE r.team_id = @team_id
  AND (@include_decided::boolean OR r.state = 'pending')
ORDER BY r.created_at DESC;

-- DecideAccessRequest is the single decision statement for both verbs: no row
-- back means it was already decided, which is the only race-free way to keep
-- "one decision per request" true when two owners click at once.
-- name: DecideAccessRequest :one
UPDATE access_requests
SET state = @state, decided_by = @decided_by, decided_by_label = @decided_by_label,
    decision_reason = @decision_reason, decided_at = now()
WHERE id = @id AND state = 'pending'
RETURNING *;

-- ListTeamOwnerEmails is who a pending request is mailed to (§6). It is the
-- membership join, not a role column on users: a panel owner's implicit
-- team-owner rank is an authorization escape hatch, never an opinion about
-- whose mailbox should receive a team's business — the same reasoning
-- ListInboxRecipients states.
-- name: ListTeamOwnerEmails :many
SELECT u.email
FROM team_members m
JOIN users u ON u.id = m.user_id
WHERE m.team_id = $1 AND m.role = 'owner'
ORDER BY u.email;
