# Feature spec: The GitHub App

> The feature matrix's **V1** row *"GitHub (App + webhooks)"*. The webhooks half
> shipped with [application-deploy.md](application-deploy.md); this is the App.
> Both reference tools have one, and it is the difference between "paste a
> repository URL and manage a deploy key" and "pick a repository".
>
> Written 2026-09-07, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. What it replaces, and what it does not

Today a private repository needs a **Deploy Key**: the panel generates a
keypair, the operator copies the public half into that repository's settings,
and the private half is sealed and handed to the builder at deploy time
([deploy-key-private-repos.md](deploy-key-private-repos.md)). That works, it is
tested, and it stays — a GitLab, Gitea or self-hosted repository has no GitHub
App and must keep working exactly as it does.

What the App changes is the GitHub case, and it changes four things:

| | Deploy key | GitHub App |
|---|---|---|
| Setup per repository | generate, copy, paste into GitHub | none — the installation already covers it |
| Choosing a repository | type the URL correctly | pick from a list |
| Credential lifetime | until someone revokes it | one hour, minted per build |
| Webhook | a per-application secret the operator wires by hand | the App delivers, one endpoint, one secret |

The last two are the security argument and they are why this is worth the
surface: a deploy key is a long-lived credential sitting in the database, and an
installation token is a one-hour credential that does not exist until a build
needs it.

## 2. The App is a panel-level credential, like the DNS provider

One App per panel, stored the way `dns_providers` already is: a singleton row,
config sealed, owner-only to write. The shape is deliberately borrowed rather
than invented — an operator who has connected Cloudflare has already met this
screen.

```
github_apps                     -- singleton, id = 1
  app_id            BIGINT      -- GitHub's numeric app id
  slug              TEXT        -- for the install URL
  config_ct/nonce   BYTEA       -- sealed: private key PEM, webhook secret, client secret

github_installations
  id                TEXT PK
  installation_id   BIGINT UNIQUE
  account_login     TEXT        -- the org or user the App is installed on
  account_type      TEXT
  repo_selection    TEXT        -- 'all' | 'selected'
```

**The private key is the whole risk**, so it is stated plainly: it can mint a
token for every repository the App is installed on. It is sealed with the master
key like every other secret (ENGINEERING rule 20), it is never returned by any
route — not even redacted — and it is unsealed in exactly one place, at exactly
one moment: minting a token for a build. That is the same discipline the deploy
key and the registry credential already follow, and the reason there is one
place is so there is one place to audit.

## 3. Installations are OBSERVED, never authored

The panel does not decide which organisations its App is installed on; GitHub
does. So installations are a **cache refreshed from the API**, exactly as
`dns_zones` caches Cloudflare's answer — and for the same reason recorded there:
an operator-entered list would be a second place to lie about what access
exists.

Refreshing lists `GET /app/installations` with an App JWT and replaces the table.
An installation GitHub no longer reports is gone; a repository the operator
removed from an installation stops appearing on the next list.

## 4. Tokens are minted, never stored

An installation access token lives one hour. Storing one would mean storing a
credential that is usually expired and occasionally valid, which is the worst
of both.

So the flow is: sign a short JWT with the App private key (`iss` = app id, ten
minutes), `POST /app/installations/{id}/access_tokens`, use the token, discard
it. Tokens are cached **in memory only**, keyed by installation, and evicted a
few minutes before expiry so a long build never carries one over the line.

A build receives it exactly as it receives a deploy key today — resolved by the
scheduler at work-build time and never persisted into the work item's history.
Nothing about the builder changes: it already knows how to clone with a
credential.

## 5. Choosing a repository

`GET /api/v1/github/repositories` lists what the installations can see,
searchable, so creating an application becomes "pick one" instead of "type a URL
and hope". The list is not cached in the database: it changes when someone adds a
repository, and a stale list that omits the repository you just made is worse
than a request.

An application records `github_installation_id` beside its existing `repo`. That
is what makes the credential resolvable at build time, and it is what
distinguishes "this GitHub repository, through the App" from "this URL, through a
deploy key" — both remain legal, and an application that names neither is a
public repository, which needs no credential at all.

## 6. Webhooks: one endpoint, one secret

The App delivers to a single URL with a single secret, verified with
`X-Hub-Signature-256` over the raw body. That is a real improvement over the
per-application secret, which the operator has to wire into each repository by
hand.

Both paths stay. The existing `POST /webhooks/github/{id}` is unchanged — it is
what a non-App repository uses, and removing it would break every application
already wired that way. The App's endpoint is new, and routes a push to
**every** application whose repo and branch match, because one repository can
legitimately be deployed by several environments.

An unverified signature is a `401` and nothing else: no lookup, no log of the
body, no hint about which applications exist. The panel's other HMAC path
already sets that precedent.

## 7. API and rank

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/github/app` | admin | whether an App is configured, its slug, the installations. Never the key |
| `PUT /api/v1/github/app` | **owner, session-only** | store the App's credentials |
| `DELETE /api/v1/github/app` | **owner, session-only** | forget it; applications using it fall back to failing honestly |
| `POST /api/v1/github/installations/refresh` | admin | re-read from GitHub |
| `GET /api/v1/github/repositories` | member | what can be deployed |
| `POST /webhooks/github/app` | none (HMAC) | the App's deliveries |

Owner and session-only to write, for the reason break glass and the agent
channel already carry: the private key can read every repository the App can
reach, and API tokens live in CI.

## 8. Deliberately out of scope

- **OAuth sign-in with GitHub.** A different feature wearing the same logo. SSO
  is `Later` in the matrix and belongs behind its own ADR.
- **Creating the App for the operator.** GitHub has a manifest flow that can
  create one from a POST. It is a genuine convenience and it is also a redirect
  dance that must be got exactly right; typing four fields once is a smaller
  thing to get wrong. Recorded as the obvious follow-up.
- **GitLab, Gitea, Bitbucket.** Their own matrix row, `V1.x`.
- **Checks / commit statuses.** Reporting a deploy back onto the commit is a
  natural second act for an App and needs its own decisions about what a failed
  deploy means for a merge. Outbound webhooks already carry the event.
- **Replacing deploy keys.** §1. They stay, and a repository outside GitHub has
  no other option.

## Implementation note — the push match was literal, and matched nothing *(fixed 2026-09-07)*

§6 says the App's single webhook deploys **every** application a push matches,
because one repository can legitimately be deployed by several environments and
picking one would silently skip the rest. That was implemented, and it never
fired, because the two sides of the comparison were never the same string.

GitHub sends `repository.full_name` — `acme/web`. An application stores what git
clones — `https://github.com/acme/web.git`. `ListApplicationsByRepo` compared
them with `=`:

```sql
WHERE a.source_repo = $1 AND a.source_branch = $2;
```

So a verified delivery returned `202 {"deployments": 0}`, GitHub drew a green
tick in Recent Deliveries, and nothing deployed. **A silent zero is the worst
shape a failure can take** — there is no error anywhere to read, on either side.
The one log line in the path fires *per application inside the loop*, so an
empty result logged nothing at all.

Worse, the only rows that could ever have matched were rows storing the bare
`acme/web` form — and those cannot clone, because the builder passes
`source_repo` to git verbatim and git reads a schemeless string as a local
directory. The matching set was therefore not "some applications are missed" but
"the only applications that could match are already broken".

**The fix canonicalises the column, not the payload.** GitHub's `full_name` is
already canonical; the stored clone URL is reduced to the same shape — trailing
slash, trailing `.git`, then scheme, `user@` and host removed — and both sides
are lowercased, because GitHub treats owner and repository names
case-insensitively. Stored URLs keep working unchanged and no migration is
needed.

The host is identified by **containing a dot**, and that detail is load-bearing
twice. It stops a bare legacy `acme/web` from having `acme/` mistaken for a host
and stripped to `web`. And it is why the expression anchors on the host rather
than taking "the last two path segments", which is the obvious version and is
wrong: it matches a nested GitLab path `grp/sub/acme/web` against a GitHub push
for `acme/web`, and **deploying an application nobody pushed to is worse than
missing one.**

`core/store/githubapp_test.go` asserts all of it against a real PostgreSQL —
eight clone-URL shapes that must match, three that must not, the branch half of
the match, and case-insensitivity. It is a real-database test because the whole
question is what a Postgres regexp does, and a fake would only assert someone's
reading of the documentation.

## Implementation note — the credential was unreachable *(fixed 2026-09-07)*

§5 says an application "records `github_installation_id` beside its existing
`repo`", and the column, the store, the scheduler and the agent all did. The
API had no such field, so nothing an operator could reach ever set one, and
`GetGitCredential()` was `nil` on every build. The App could be connected,
installed and listed, and no application could use it.

Four things were wrong, and only the first was obvious.

**The field was absent from the contract and both write paths.** `AppSource` in
`openapi.yaml` listed six fields and not this one, and all three Go mappers in
`handlers_applications.go` enumerated the same six literally, so create and
patch both dropped it. It is now on the schema — one edit, because `Application`,
`CreateApplicationRequest` and `PatchApplicationRequest` all `$ref` it — and on
all three mappers.

**PATCH actively destroyed it.** `Update` replaces `Source` wholesale and the
SQL writes `github_installation_id = $31` unconditionally, so a value inserted by
hand was wiped by the first save on the settings screen — which always sends the
whole `source` object. Adding the field to the patch DTO is what fixes that; a
test now asserts a `source`-carrying PATCH preserves it, and that clearing it
still works, because moving an application *off* the App is the reverse of the
migration this enables.

**Preview environments dropped it.** `core/previews` built the cloned
application's source field by field and copied only `kind`, `repo`, `branch` and
`deploy_key_id`. So a preview of an App-sourced private application cloned
anonymously and failed its first build. It now carries the installation *and*
the pull registry, for the same reason: a preview of a private application is a
build of a private repository.

**Nothing was refused at save time.** An installation the panel does not have is
now a 400 that names the remedy — install the App on that account — instead of a
token-minting failure five minutes into a build. The check is against the
observed installation cache, which is the only honest source: it is GitHub's
answer, refreshed, never authored.

**What was deliberately NOT done.** An earlier draft refused
`github_installation_id` and `deploy_key_id` together. §5 says "both remain
legal", the builder already has a precedence — the App credential, else the
deploy key — and because `Update` re-validates the *merged* result, refusing the
combination would have `400`-ed a PATCH on an application that already carried
both. The settings screen names which one wins instead, which is the honest
treatment of a legal combination.

**The picker sets the clone URL, not `full_name`.** `GET /github/repositories`
reports both; `acme/web` is refused by the repository validation for the same
reason the create dialog stopped suggesting it — git reads a schemeless string
as a local directory. The picker sets `clone_url` and the repository's default
branch.
