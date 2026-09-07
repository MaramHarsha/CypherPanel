-- The GitHub App (github-app.md §2). Singleton credential on dns_providers'
-- shape, with an observed installation cache beside it.

-- name: GetGitHubApp :one
SELECT * FROM github_apps WHERE id = 1;

-- name: SetGitHubApp :exec
INSERT INTO github_apps (id, app_id, slug, config_ct, config_nonce, updated_at)
VALUES (1, $1, $2, $3, $4, now())
ON CONFLICT (id) DO UPDATE
SET app_id = EXCLUDED.app_id, slug = EXCLUDED.slug,
    config_ct = EXCLUDED.config_ct, config_nonce = EXCLUDED.config_nonce,
    updated_at = now();

-- name: DeleteGitHubApp :exec
DELETE FROM github_apps WHERE id = 1;

-- name: ListGitHubInstallations :many
SELECT * FROM github_installations ORDER BY account_login;

-- DeleteGitHubInstallationsNotIn is how a refresh lands: GitHub's answer is the
-- whole truth, so an installation it no longer reports is no longer ours (§3).
-- name: DeleteGitHubInstallationsNotIn :exec
DELETE FROM github_installations WHERE installation_id <> ALL(@ids::bigint[]);

-- name: UpsertGitHubInstallation :exec
INSERT INTO github_installations (id, installation_id, account_login, account_type, repo_selection, refreshed_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (installation_id) DO UPDATE
SET account_login = EXCLUDED.account_login, account_type = EXCLUDED.account_type,
    repo_selection = EXCLUDED.repo_selection, refreshed_at = now();

-- ListApplicationsByRepo finds every application a push should deploy. EVERY
-- one, deliberately: a repository can legitimately be deployed by several
-- environments, and picking one would silently skip the rest (§6).
--
-- THE COMPARISON IS CANONICAL, NOT LITERAL, and that is the whole point of this
-- query. GitHub sends `full_name` — `acme/web` — while `source_repo` holds what
-- git clones, which is `https://github.com/acme/web.git`. Comparing those two
-- with `=` matched nothing, so a verified push answered `202 {"deployments":0}`,
-- GitHub drew a green tick in Recent Deliveries, and nothing deployed. A silent
-- zero is the worst shape a failure can take: there is no error to read.
--
-- The expression strips a trailing slash, a trailing `.git`, and then the
-- scheme, any `user@`, and the HOST — identified by containing a dot, which is
-- what keeps a bare legacy `acme/web` from having `acme/` mistaken for a host
-- and stripped. Anchoring on the host rather than taking "the last two
-- segments" is deliberate: the lazy version matches a nested GitLab path
-- `grp/sub/acme/web` against a GitHub push for `acme/web`, and deploying an
-- application nobody pushed to is worse than missing one. It therefore matches
-- `https://github.com/acme/web`, `...web.git`, `ssh://git@github.com/acme/web`
-- and `git@github.com:acme/web.git` alike, and leaves a bare legacy `acme/web`
-- untouched so rows written before repository shapes were validated still
-- match. Both sides are lowercased because GitHub treats owner and repository
-- names case-insensitively.
--
-- name: ListApplicationsByRepo :many
SELECT a.* FROM applications a
WHERE lower(regexp_replace(regexp_replace(regexp_replace(btrim(a.source_repo), '/+$', ''), '\.git$', ''),
                        '^([a-z][a-z0-9+.-]*://)?([^/@]+@)?[^/:]*\.[^/:]*[:/]', '')) = lower(sqlc.arg(full_name))
  AND a.source_branch = sqlc.arg(branch);
