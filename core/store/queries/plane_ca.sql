-- name: GetPlaneCA :one
SELECT * FROM plane_ca WHERE id = 1;

-- name: InsertPlaneCA :exec
INSERT INTO plane_ca (id, cert_pem, encrypted_key, key_nonce)
VALUES (1, $1, $2, $3);
