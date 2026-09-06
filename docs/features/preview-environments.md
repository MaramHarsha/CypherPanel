# Feature spec: Preview environments

> A pull request opens; the panel spins up a full, isolated copy of the
> Application built from the PR's branch, reachable at its own preview
> subdomain. The PR closes (or a TTL elapses); the panel tears it all down.
> No manual step, either way. This completes the Phase 3 acceptance gate
> ([roadmap.md](../roadmap.md)).
>
> Written 2026-07-20, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Builds directly on the Phase 2 deploy pipeline
> (`application-deploy.md`), routing (`routing-and-tls.md`), and the existing
> per-Application GitHub webhook.

## 1. The core idea: a preview is just an Environment

The roadmap's design bet ([architecture.md](../architecture.md) key flows):
because **Environments are first-class rows**, a preview is not a special
case — it is an ordinary child Environment holding an ordinary Application,
created and destroyed automatically from PR lifecycle events. Everything that
already works for a normal app (build → health-gated rollout → routed through
the Proxy → logs → teardown) works for a preview unchanged. This spec adds
only the *automation* around that: the trigger, the templating, and the
destroy.

```
Project "acme"
├── Environment "production"   ← the source Application lives here
├── Environment "pr-42"        ← created when PR #42 opens
│     └── Application "web-pr-42"  (branch = PR head, domain = pr-42.<base>)
└── Environment "pr-57"        ← created when PR #57 opens
      └── Application "web-pr-57"
```

## 2. Opt-in configuration (on the source Application)

Previews are per-Application opt-in. New fields on the source Application:

| Field                 | Meaning |
|-----------------------|---------|
| `preview_enabled`     | Master switch. Default false. |
| `preview_base_domain` | Wildcard base, e.g. `preview.acme.com`. A PR gets `pr-<n>.<base>`. Required when enabled. |
| `preview_ttl_hours`   | Auto-destroy backstop (default 72). A preview past its TTL is swept even if the close event was missed. |

The operator points a **wildcard DNS record** (`*.preview.acme.com`) at the
serving host and lets each preview obtain its own certificate via the existing
Traefik `le` HTTP-01 resolver — no wildcard cert, no DNS-01 needed
(routing-and-tls.md §7). Previews are HTTPS when the base domain is set and
the agent has an ACME email; otherwise HTTP.

## 3. The resource model

A **Preview** row tracks one live preview and links the automation to the
first-class rows it created:

```
Preview:
  id                TEXT PK (prv_ prefix)
  source_app_id     TEXT NOT NULL → applications(id) ON DELETE CASCADE
  environment_id    TEXT NOT NULL → environments(id)  ON DELETE CASCADE  -- the child env
  preview_app_id    TEXT          → applications(id)  ON DELETE SET NULL -- the cloned app
  pr_number         INTEGER NOT NULL
  pr_branch         TEXT NOT NULL      -- PR head ref
  domain            TEXT NOT NULL      -- pr-<n>.<base>
  status            TEXT NOT NULL      -- creating | running | error | destroying
  expires_at        TIMESTAMPTZ        -- created_at + preview_ttl_hours
  created_at, updated_at
  UNIQUE(source_app_id, pr_number)
```

`status` is orchestration state (is the preview being set up / torn down),
distinct from the cloned Application's own observation-driven status — the
preview app reports running/error through the normal ADR-005 path.

## 4. Lifecycle — driven by the existing webhook

The per-Application webhook (`POST /webhooks/github/{id}`) already
authenticates GitHub deliveries by per-app HMAC and handles `push`. It gains a
second event type: `pull_request`. Same endpoint, same auth, no new secret.

### PR opened / reopened / synchronize

1. Authenticate (existing HMAC). Parse the `pull_request` payload for action,
   number, head ref, head SHA.
2. Only act when the **source Application has `preview_enabled`** and the PR
   targets the app's configured base branch (a preview of a PR *into* main;
   PRs between feature branches are ignored at v1).
3. **No existing Preview for (source_app, pr_number)** → provision:
   - Create child Environment `pr-<n>` in the source app's Project.
   - **Clone** the source Application into it (§5) with branch = PR head ref
     and domain = `pr-<n>.<base>`.
   - Insert the Preview row (`status=creating`, `expires_at` set).
   - Trigger a deploy of the cloned app at the PR head SHA (the normal
     scheduler pipeline).
4. **Existing Preview** (a `synchronize` push of new commits) → redeploy the
   cloned app at the new head SHA. No new env/app.

### PR closed (merged or not)

Destroy, idempotently: mark `status=destroying` → `RemoveApp` (agent tears
down the container + route fragment, desired-state GC reclaims the image) →
delete the cloned Application row → delete the child Environment row → delete
the Preview row. Teardown is asynchronous, so a close is accepted with 202;
a close for an unknown PR is a harmless no-op (also 202).

### TTL backstop

A **sweeper** (same pattern as the heartbeat-stale sweeper) periodically finds
previews past `expires_at` and runs the same destroy path. This covers the
webhook that never arrives (PR deleted, repo permissions changed, delivery
lost) so previews can't leak forever — the #1 preview-environment footgun in
the references (research/community-pain-points.md).

## 5. Cloning the Application

The cloned preview Application copies the source's `Source` (repo, deploy
key), `Build` (dockerfile, context), `Runtime` (server, port, replicas), and
`Health` verbatim, and **overrides**:

- `Source.Branch` = PR head ref (the code under review).
- `Route.Domain` = `pr-<n>.<base>`; `Route.HTTPS` **inherits the source app's
  TLS posture** (an HTTPS production app — real domain + ACME — yields HTTPS
  previews under the wildcard base; an HTTP app yields HTTP previews).
- `Name` = `<source-name>-pr-<n>` (deterministic, human-readable).
- A fresh webhook id + sealed secret (created by the normal app-create path;
  unused by the preview — deploys are driven by the *source* app's webhook —
  but the schema requires it and it costs nothing).

The clone reuses the source's env vars? **No at v1** — preview env vars are a
follow-on (secrets in previews are a fork-PR exfiltration risk, threat-model
§5.10: a malicious PR could read them). Previews run with no app env vars
until per-preview scoping is designed. Documented in §8.

## 6. Isolation & security

- **Network isolation** is automatic: each preview app lives in
  `cypher-<preview-env-id>`, a different Docker network from production and
  from other previews (same mechanism as any environment).
- **Fork-PR secrets** (threat-model §5.10): no env vars are injected into
  previews at v1 (§5), so a fork PR that changes the Dockerfile to exfiltrate
  cannot read production secrets — there are none in the preview. Deploy keys
  *are* needed to clone a private source repo; that key is the source's
  already-trusted read key, used only to fetch the reviewed branch, never
  exposed to the built image beyond the clone (deploy-key-private-repos.md §4).
- **Webhook auth** is unchanged: the per-app HMAC secret gates PR events just
  as it gates pushes; an unauthenticated caller can neither deploy nor spawn
  previews.
- **Resource caps**: previews inherit the source's CPU/memory limits; a noisy
  fleet of previews is bounded by the TTL and by the operator not enabling
  previews on unlimited apps. Per-environment preview count caps are a
  follow-on (§8).
- **Audited in both directions** ([audit-log.md](audit-log.md) §4): a preview
  environment appears and disappears with nobody signed in, so the manager
  writes `environment.created` and `environment.deleted` with a `system` actor
  labelled *preview automation*, carrying the PR number and the reason (*pull
  request closed* / *ttl expired*). The manual `DELETE` is audited by its
  handler with the operator's name. Without these rows an environment could
  come and go with no record that it existed.

## 7. API surface (under `/api/v1`)

Previews are created by the webhook, not by hand, but they are observable and
manually destroyable:

```
GET    /applications/{id}/previews          → [Preview]   (previews of a source app)
GET    /previews/{id}                        → Preview
DELETE /previews/{id}                        → 202         (manual teardown; same destroy path)
```

Preview config rides on the Application create/patch bodies
(`preview_enabled`, `preview_base_domain`, `preview_ttl_hours`).

## 8. Acceptance (testable)

1. Enable previews on an app; deliver a signed `pull_request: opened` webhook →
   a child Environment `pr-<n>` and a cloned Application appear, the app
   deploys, and it is reachable at `pr-<n>.<base>` through the Proxy.
2. Deliver `synchronize` for the same PR → the preview app redeploys the new
   SHA; no second environment is created.
3. Deliver `pull_request: closed` → the container, route, cloned app,
   environment, and Preview row are all gone; production is untouched.
4. A preview past its `preview_ttl_hours` with no close event → the sweeper
   destroys it.
5. A `pull_request` webhook for an app with previews disabled, or with a bad
   signature → no preview, correct status (204 / 401).
6. Two open PRs → two isolated previews on different subdomains and networks;
   destroying one leaves the other running.

## 9. Out of scope this slice

Preview env vars / per-preview secret scoping · per-environment preview count
caps · scaled-down preview resources (previews inherit source limits) ·
preview comments posted back to the PR · non-GitHub PR sources (GitLab/Gitea) ·
previews for PRs between non-base branches · wildcard (DNS-01) certificates
(per-subdomain HTTP-01 is used).
