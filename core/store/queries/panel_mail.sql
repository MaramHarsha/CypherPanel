-- name: GetPanelMail :one
SELECT config_ct, config_nonce, updated_at FROM panel_mail WHERE id = 1;

-- name: SetPanelMail :exec
INSERT INTO panel_mail (id, config_ct, config_nonce, updated_at)
VALUES (1, $1, $2, now())
ON CONFLICT (id) DO UPDATE
SET config_ct = EXCLUDED.config_ct, config_nonce = EXCLUDED.config_nonce, updated_at = now();

-- name: DeletePanelMail :exec
DELETE FROM panel_mail WHERE id = 1;
