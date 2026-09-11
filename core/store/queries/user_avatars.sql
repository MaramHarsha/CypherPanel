-- name: SetUserAvatar :exec
INSERT INTO user_avatars (user_id, content_type, bytes, etag, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id) DO UPDATE
SET content_type = EXCLUDED.content_type,
    bytes        = EXCLUDED.bytes,
    etag         = EXCLUDED.etag,
    updated_at   = now();

-- name: GetUserAvatar :one
SELECT content_type, bytes, etag, updated_at FROM user_avatars WHERE user_id = $1;

-- name: DeleteUserAvatar :exec
DELETE FROM user_avatars WHERE user_id = $1;
