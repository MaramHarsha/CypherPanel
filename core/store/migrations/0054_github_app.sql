-- The GitHub App (github-app.md §2).
--
-- Shaped on dns_providers deliberately rather than invented: one panel-level
-- credential, sealed, owner-only to write, with an observed cache beside it.
-- An operator who has connected Cloudflare has already met this screen.
--
-- THE PRIVATE KEY IS THE WHOLE RISK and it is stated rather than implied: it
-- can mint a token for every repository the App is installed on. It is sealed
-- with the master key like every other secret (ENGINEERING rule 20), it is
-- never returned by any route — not even redacted — and it is unsealed in
-- exactly one place, at exactly one moment: minting a token for a build.
--
-- +goose Up
CREATE TABLE github_apps (
    id         SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    app_id     BIGINT      NOT NULL,
    slug       TEXT        NOT NULL DEFAULT '',
    -- Sealed: the private key PEM and the webhook secret.
    config_ct    BYTEA     NOT NULL,
    config_nonce BYTEA     NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Installations are OBSERVED, never authored: the panel does not decide which
-- organisations its App is installed on, GitHub does. This is a cache of the
-- API's answer, exactly as dns_zones caches Cloudflare's — an operator-entered
-- list would be a second place to lie about what access exists (§3).
CREATE TABLE github_installations (
    id              TEXT        PRIMARY KEY,
    installation_id BIGINT      NOT NULL UNIQUE,
    account_login   TEXT        NOT NULL,
    account_type    TEXT        NOT NULL DEFAULT '',
    repo_selection  TEXT        NOT NULL DEFAULT 'selected',
    refreshed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Which installation an application's repository is reached through. NULL is
-- every application that exists today: a deploy key, or a public repository,
-- both of which keep working unchanged (§1).
--
-- ON DELETE SET NULL rather than RESTRICT: an installation the operator removed
-- on GitHub is already gone, and refusing to refresh the cache because an
-- application still references it would leave the panel lying about its access
-- to protect a foreign key. The application then fails its next deploy with a
-- reason, which is the honest outcome.
ALTER TABLE applications
    ADD COLUMN github_installation_id BIGINT
        REFERENCES github_installations(installation_id) ON DELETE SET NULL;

CREATE INDEX idx_applications_github_installation ON applications (github_installation_id)
    WHERE github_installation_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_applications_github_installation;
ALTER TABLE applications DROP COLUMN github_installation_id;
DROP TABLE github_installations;
DROP TABLE github_apps;
