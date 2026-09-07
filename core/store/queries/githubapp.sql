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
-- name: ListApplicationsByRepo :many
SELECT a.* FROM applications a
WHERE a.source_repo = $1 AND a.source_branch = $2;
