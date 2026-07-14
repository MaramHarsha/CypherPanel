-- Latest host snapshot per server (current state, not history — time-series
-- metrics go to a dedicated store per plan.md Section 8).

ALTER TABLE servers
    ADD COLUMN load_1m            double precision NOT NULL DEFAULT 0,
    ADD COLUMN memory_total_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN memory_used_bytes  bigint NOT NULL DEFAULT 0,
    ADD COLUMN disk_total_bytes   bigint NOT NULL DEFAULT 0,
    ADD COLUMN disk_used_bytes    bigint NOT NULL DEFAULT 0;
