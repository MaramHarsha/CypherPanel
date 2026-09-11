-- +goose Up
-- Initial application database for a managed engine (managed-databases.md §2).
-- The engine images honour POSTGRES_DB / MYSQL_DATABASE / MARIADB_DATABASE /
-- MONGO_INITDB_DATABASE only while initializing an empty data directory, so
-- this is set once at creation and never patched.
--
-- Additive with a default (ENGINEERING rule 16): existing databases read as ''
-- and keep behaving exactly as they do today — PostgreSQL on its `postgres`
-- database, the rest with none.

ALTER TABLE databases ADD COLUMN initial_database TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE databases DROP COLUMN initial_database;
