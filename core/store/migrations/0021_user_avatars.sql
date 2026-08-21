-- +goose Up
-- Profile photos (canvas 7a/13i), in the database rather than on disk: the
-- panel's durable truth is Postgres, so an avatar stored here is backed up,
-- restored and replicated by everything that already handles the rest of the
-- state — and it survives the container being replaced, which DATA_DIR does
-- not unless someone remembered to mount it.
--
-- Its own table, not a column on users: every authentication reads the user row
-- with SELECT *, and a bytea there would drag the image through each one. The
-- row is deleted with its owner.
CREATE TABLE user_avatars (
    user_id      TEXT        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    content_type TEXT        NOT NULL,
    bytes        BYTEA       NOT NULL,
    etag         TEXT        NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE user_avatars;
