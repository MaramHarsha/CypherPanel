-- name: GetMailProvider :one
SELECT * FROM mail_provider WHERE id = 1;

-- name: SetMailProvider :one
INSERT INTO mail_provider (id, kind, config_ct, config_nonce)
VALUES (1, $1, $2, $3)
ON CONFLICT (id) DO UPDATE
SET kind = EXCLUDED.kind, config_ct = EXCLUDED.config_ct,
    config_nonce = EXCLUDED.config_nonce, updated_at = now()
RETURNING *;

-- name: DeleteMailProvider :exec
DELETE FROM mail_provider WHERE id = 1;

-- name: CreateMailDomain :one
INSERT INTO mail_domains (id, domain) VALUES ($1, $2)
ON CONFLICT (domain) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: GetMailDomain :one
SELECT * FROM mail_domains WHERE id = $1;

-- name: GetMailDomainByName :one
SELECT * FROM mail_domains WHERE domain = $1;

-- name: ListMailDomains :many
SELECT * FROM mail_domains ORDER BY domain;

-- name: SetMailDomainRecords :exec
UPDATE mail_domains SET records_written_at = $2, last_error = $3, updated_at = now() WHERE id = $1;

-- name: DeleteMailDomain :exec
DELETE FROM mail_domains WHERE id = $1;

-- name: CreateMailboxLink :one
INSERT INTO mailbox_links (id, domain_id, address, user_id)
VALUES ($1, $2, $3, sqlc.narg('user_id'))
ON CONFLICT (address) DO UPDATE SET user_id = EXCLUDED.user_id
RETURNING *;

-- name: ListMailboxLinks :many
SELECT * FROM mailbox_links WHERE domain_id = $1 ORDER BY address;

-- name: DeleteMailboxLink :exec
DELETE FROM mailbox_links WHERE address = $1;
