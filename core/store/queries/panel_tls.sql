-- Panel TLS settings (agent-identity-and-tls.md §4). A singleton on
-- panel_mail's shape; the values are public (see the migration), so there is
-- no _ct/_nonce pair here.

-- name: GetPanelTLS :one
SELECT acme_email, acme_ca_server, updated_at FROM panel_tls WHERE id = 1;

-- name: SetPanelTLS :exec
INSERT INTO panel_tls (id, acme_email, acme_ca_server, updated_at)
VALUES (1, $1, $2, now())
ON CONFLICT (id) DO UPDATE
SET acme_email = EXCLUDED.acme_email,
    acme_ca_server = EXCLUDED.acme_ca_server,
    updated_at = now();

-- name: DeletePanelTLS :exec
DELETE FROM panel_tls WHERE id = 1;
