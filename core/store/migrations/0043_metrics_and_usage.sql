-- Metrics and usage (metrics-and-usage.md §6).
--
-- NO FOREIGN KEYS on any of these, for two reasons. resource_kind is
-- polymorphic — an Application, a Compose Stack, a Managed Database or a Server
-- — so there is no single column a key could point at. And more importantly a
-- cascade would REWRITE HISTORY: delete an application on the 28th and every
-- cascading row for that month vanishes, so a monthly report run on the 1st is
-- smaller than the same report run on the 27th, with no record that anything
-- was removed.
--
-- The daily rows therefore SNAPSHOT the ownership chain — team, project name,
-- environment name, resource name — taken when the row is written, exactly as
-- an audit event snapshots what it names. The short-lived bucket tables carry
-- no names at all; they are swept by retention and joined to live resources on
-- read, so a bucket whose resource is gone is simply never selected.

-- +goose Up
CREATE TABLE resource_metrics (
    resource_kind       TEXT NOT NULL,
    resource_id         TEXT NOT NULL,
    bucket_start        TIMESTAMPTZ NOT NULL,
    server_id           TEXT NOT NULL,
    -- Accumulators, never means: a mean is derived by dividing by
    -- covered_seconds, so a longer window is exact rather than an
    -- average-of-averages.
    cpu_core_ms         BIGINT NOT NULL DEFAULT 0,
    cpu_percent_peak    DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_byte_seconds BIGINT NOT NULL DEFAULT 0,
    -- Peaks alongside means: without them a 5-minute mean hides exactly the
    -- spike an operator opened the page to find.
    memory_bytes_peak   BIGINT NOT NULL DEFAULT 0,
    memory_limit_bytes  BIGINT NOT NULL DEFAULT 0,
    sample_count        INTEGER NOT NULL DEFAULT 0,
    covered_seconds     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (resource_kind, resource_id, bucket_start)
);

CREATE INDEX idx_resource_metrics_sweep ON resource_metrics(bucket_start);

CREATE TABLE resource_disk_usage (
    resource_kind   TEXT NOT NULL,
    resource_id     TEXT NOT NULL,
    bucket_start    TIMESTAMPTZ NOT NULL,
    server_id       TEXT NOT NULL,
    image_bytes     BIGINT NOT NULL DEFAULT 0,
    volume_bytes    BIGINT NOT NULL DEFAULT 0,
    container_bytes BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (resource_kind, resource_id, bucket_start)
);

CREATE INDEX idx_resource_disk_sweep ON resource_disk_usage(bucket_start);

CREATE TABLE request_metrics (
    resource_kind     TEXT NOT NULL,
    resource_id       TEXT NOT NULL,
    bucket_start      TIMESTAMPTZ NOT NULL,
    server_id         TEXT NOT NULL,
    requests          BIGINT NOT NULL DEFAULT 0,
    redirect_count    BIGINT NOT NULL DEFAULT 0,
    status_2xx        BIGINT NOT NULL DEFAULT 0,
    status_3xx        BIGINT NOT NULL DEFAULT 0,
    status_4xx        BIGINT NOT NULL DEFAULT 0,
    status_5xx        BIGINT NOT NULL DEFAULT 0,
    response_bytes    BIGINT NOT NULL DEFAULT 0,
    -- 16 fixed boundaries. Histograms add, so p50/p95/p99 over any window are
    -- computed once from the summed histogram; percentiles do not average.
    latency_buckets   BIGINT[] NOT NULL DEFAULT '{}',
    histogram_version INTEGER NOT NULL DEFAULT 1,
    sample_rate       INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (resource_kind, resource_id, bucket_start)
);

CREATE INDEX idx_request_metrics_sweep ON request_metrics(bucket_start);

CREATE TABLE request_paths (
    resource_kind     TEXT NOT NULL,
    resource_id       TEXT NOT NULL,
    bucket_start      TIMESTAMPTZ NOT NULL,
    path              TEXT NOT NULL,
    requests          BIGINT NOT NULL DEFAULT 0,
    status_5xx        BIGINT NOT NULL DEFAULT 0,
    latency_buckets   BIGINT[] NOT NULL DEFAULT '{}',
    histogram_version INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (resource_kind, resource_id, bucket_start, path)
);

CREATE INDEX idx_request_paths_sweep ON request_paths(bucket_start);

CREATE TABLE resource_usage_daily (
    resource_kind       TEXT NOT NULL,
    resource_id         TEXT NOT NULL,
    day                 DATE NOT NULL,
    -- The snapshotted ownership chain. Names, not just ids: a project renamed
    -- in March must still read as it did in February's report.
    team_id             TEXT NOT NULL DEFAULT '',
    project_id          TEXT NOT NULL DEFAULT '',
    project_name        TEXT NOT NULL DEFAULT '',
    environment_id      TEXT NOT NULL DEFAULT '',
    environment_name    TEXT NOT NULL DEFAULT '',
    resource_name       TEXT NOT NULL DEFAULT '',
    cpu_core_seconds    BIGINT NOT NULL DEFAULT 0,
    memory_byte_seconds BIGINT NOT NULL DEFAULT 0,
    memory_bytes_peak   BIGINT NOT NULL DEFAULT 0,
    disk_bytes          BIGINT NOT NULL DEFAULT 0,
    requests            BIGINT NOT NULL DEFAULT 0,
    status_5xx          BIGINT NOT NULL DEFAULT 0,
    deploy_count        INTEGER NOT NULL DEFAULT 0,
    deploy_seconds      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (resource_kind, resource_id, day)
);

CREATE INDEX idx_resource_usage_daily_day ON resource_usage_daily(day);
CREATE INDEX idx_resource_usage_daily_project ON resource_usage_daily(project_id, day);

-- Deploy minutes must mean build-and-rollout time, not wall time since somebody
-- clicked deploy: without this the figure would include queue time and, worse,
-- the hours a deploy sat awaiting approval — a project would be charged for its
-- own change-management policy. Rows predating this migration are null and fall
-- back to created_at, which the CSV documents as an upper bound.
ALTER TABLE deployments ADD COLUMN started_at TIMESTAMPTZ;

-- Panel-wide collection policy, carried to every node in desired state the way
-- the ACME account is. Request analytics is OFF by default: paths are
-- application-authored data, and an operator for whom URLs are themselves
-- sensitive must not have that decision made for them.
CREATE TABLE metrics_settings (
    id                INTEGER PRIMARY KEY DEFAULT 1,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    request_analytics BOOLEAN NOT NULL DEFAULT false,
    bucket_seconds    INTEGER NOT NULL DEFAULT 300,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT metrics_settings_singleton CHECK (id = 1),
    -- A bucket that does not divide an hour makes the hourly path and disk
    -- rows land in two different buckets.
    CONSTRAINT metrics_settings_bucket CHECK (bucket_seconds > 0 AND 3600 % bucket_seconds = 0)
);

INSERT INTO metrics_settings (id) VALUES (1);

-- +goose Down
DROP TABLE metrics_settings;
ALTER TABLE deployments DROP COLUMN started_at;
DROP TABLE resource_usage_daily;
DROP TABLE request_paths;
DROP TABLE request_metrics;
DROP TABLE resource_disk_usage;
DROP TABLE resource_metrics;
