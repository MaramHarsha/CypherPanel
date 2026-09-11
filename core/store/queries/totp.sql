-- name: SetTOTPSecret :exec
-- Store (or replace) the enrolling secret; enrollment is not yet active.
UPDATE users
SET totp_secret_enc = $2, totp_secret_nonce = $3, totp_enabled = false
WHERE id = $1;

-- name: EnableTOTP :exec
UPDATE users SET totp_enabled = true WHERE id = $1;

-- name: DisableTOTP :exec
UPDATE users
SET totp_secret_enc = NULL, totp_secret_nonce = NULL, totp_enabled = false
WHERE id = $1;

-- name: GetTOTPSecret :one
SELECT totp_secret_enc, totp_secret_nonce, totp_enabled FROM users WHERE id = $1;

-- name: AddRecoveryCode :exec
INSERT INTO totp_recovery_codes (id, user_id, code_hash) VALUES ($1, $2, $3);

-- name: ConsumeRecoveryCode :one
-- Marks the matching unused code used; returns its id so the caller knows a code
-- was actually consumed (no row ⇒ wrong or already-spent code).
UPDATE totp_recovery_codes
SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
RETURNING id;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM totp_recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- name: DeleteRecoveryCodes :exec
DELETE FROM totp_recovery_codes WHERE user_id = $1;
