# Feature spec: Project export and account deletion

> Two answers to one question — *can I leave?* **Project export** produces a
> single archive holding the definition of every Application, Managed Database
> and Compose Stack in a Project, in a form that runs anywhere Docker runs.
> **Account deletion** lets a person remove themselves, refusing only while
> they are the last owner of a team, and leaving the audit log intact.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Design source: canvas `14k`
> (`SETTINGS / EXPORT`), whose copy is quoted where it is load-bearing.
>
> It makes true a promise the product has been making in prose:
> [vision.md](../vision.md) says success looks like *"a team migrates off
> Coolify or Dokploy and loses zero capabilities they used"*, and the same
> claim in reverse has never been implemented. It also discharges the last
> unbacked line of [audit-log.md](audit-log.md) §10 — *"14k, export & leave:
> audit entries stay"* — which that spec asserted as a property without a
> feature to exercise it.

## 1. Why leaving is a feature

An export is the cheapest honest signal a self-hosted product can send. Every
lock-in argument the panel could make — proprietary state, an undocumented
schema, a config you can only read through our UI — would be an argument
against [vision.md](../vision.md)'s own framing: *"the API is good enough that
someone builds a competing UI on top of it — and that's fine."* A panel that is
easy to leave is a panel you can adopt without a meeting.

The design screen states the bargain plainly: *"Runs anywhere Docker runs —
leaving is allowed to be easy."* That sentence is the specification. It rules
out two designs that would otherwise be tempting:

- **A CypherPanel-shaped dump.** A JSON blob of our rows, restorable only by a
  future CypherPanel. That is a backup of our database, not an exit; it would
  make the archive useless on the day the operator actually needs it.
- **A Template.** [ADR-007](../adrs/ADR-007-template-format.md) gives us a
  native declarative schema that already describes Applications and Managed
  Databases, and reusing it here would cost almost nothing. Rejected for the
  same reason: a Template is an *install-time* definition with generated
  secrets and placeholders that only resolves inside CypherPanel. It is the
  format for arriving, not for leaving.

What is left is compose. It is the one format every reader of this archive
already has an interpreter for, and it is the format both references' users
arrived with.

The screen also pairs the export with **account deletion**, and the pairing is
deliberate rather than layout convenience: the two questions *"how do I take my
work with me"* and *"how do I go"* are asked in the same breath, and putting
the button that removes you on a different screen from the archive you should
take first is how a product loses someone's data politely. They stay on one
page. They remain two unrelated operations — §7 is explicit that deleting an
account deletes no project.

## 2. What the archive is, and what it is deliberately not

**It is a definition, not a backup.** The archive carries configuration:
services, images, ports, volumes *declarations*, domains, schedules, and the
*names* of the environment variables each resource expects. It carries no
volume contents, no database dump, no build cache and no image layers.

That boundary is not a shortcut, it is the only defensible line. Database
contents already have a feature that does this properly — scheduled dumps to a
**Backup Target** with restore, resume and progress
([managed-databases.md](managed-databases.md) §7) — and a second, silent,
unversioned copy of the same bytes inside a settings-page download would be a
worse version of it that an operator would trust equally. The README in the
archive says so in one line, and the UI says it above the button.

For the same reason the word **backup** never appears in this feature's copy,
API or identifiers. `Backup Target` and `Database Backup` are taken
([glossary.md](../glossary.md)), and letting "export" drift toward meaning
"backup" is exactly the vocabulary poisoning the glossary exists to prevent.
The glossary gains one entry with this spec:

> **Project Export** — one archive containing the definition of every Resource
> in a Project: a compose file per Environment, the environment-variable *keys*
> each Resource expects, its domains, volumes and schedules, and a machine-readable
> manifest. It carries no secret value and no volume data. It is not a Backup.

**It is a snapshot of desired state, never of observed state.** No `status`,
no health, no last-deployed timestamp appears in the archive. Under
[ADR-005](../adrs/ADR-005-desired-state-reconciliation.md) an observation
belongs to the agent that made it and is meaningless on another operator's
host; a file claiming `status: running` about a container that has never
existed there is a lie with a straight face.

## 3. The archive

`GET /api/v1/projects/{id}/export` streams `application/gzip` with
`Content-Disposition: attachment; filename="<project-slug>-<YYYY-MM-DD>.tar.gz"`.
The slug is the immutable per-team handle
([projects-and-environments.md](projects-and-environments.md) §2), so the
filename does not change when the project is renamed.

```
acme-website/
├── README.md
├── cypherpanel.yaml
├── production/
│   ├── docker-compose.yml
│   ├── env/
│   │   ├── api.env.example
│   │   ├── worker.env.example
│   │   └── postgres.env.example
│   └── stacks/
│       └── plausible.yml
└── staging/
    ├── docker-compose.yml
    └── env/…
```

**One directory per Environment**, because an Environment is where the panel's
own network boundary is (`cypher-<environment_id>`), and merging production and
staging into one compose project would put them on one network — a difference
nobody would notice until it mattered.

**Preview environments are excluded.** A preview belongs to its pull request
and is destroyed by a close or a TTL sweep
([preview-environments.md](preview-environments.md)); exporting one produces
services whose reason to exist expired before the download finished. The
manifest records the source Application's preview settings instead, which is
the part that is actually configuration.

**`README.md` is the part a human reads first**, and it is generated per
project rather than boilerplate: the exact commands to run each Environment and
each Compose Stack, the table of domain → service → port that §3.5 leaves to
the operator's own proxy, the list of env files to fill in, the panel volume
names paired with their compose names, and — plainly, at the top — the four
things this archive does not contain (secret values, volume and database data,
TLS certificates, and images). It is the archive's answer to ui-principles §11's
"no dead ends", written for a screen the panel will never render.

### 3.1 `cypherpanel.yaml` — the lossless half

The manifest is the machine-readable record: everything the panel knows about
the project that a compose file cannot express. It is keyed by
`export_version: 1` and carries the `cypherpanel_version` that wrote it.

```yaml
export_version: 1
cypherpanel_version: v0.9.3
project:
  id: prj_…            # the panel's ids are kept: they are what an
  slug: acme-website   # audit entry, a webhook URL and a support
  name: Acme website   # question all name
  team: { id: tm_…, name: Acme }
environments:
  - name: production
    kind: production
    network: cypher-env_7f3…          # what the panel called it
    applications:
      - name: api
        id: app_…
        server: { id: srv_…, name: hetzner-fsn1 }   # where it ran — never "destination"
        source:
          kind: github
          repo: git@github.com:acme/api.git
          branch: main
          deployed_commit: 4f2c1ab…   # what was actually serving
          deploy_key: { name: acme-ci, fingerprint: "SHA256:…" }
        build: { kind: dockerfile, dockerfile: ./Dockerfile, context: . }
        runtime: { port: 8080, replicas: 2, cpu_limit: 1.0, memory_limit_mb: 512 }
        route: { domain: api.acme.com, https: true, path_prefix: "" }
        health: { kind: http, path: /healthz, interval_seconds: 10, timeout_seconds: 5, retries: 3 }
        volumes: [{ name: uploads, path: /app/uploads, panel_volume: cypher-app_…-uploads }]
        ports: [{ host_port: 8443, container_port: 8443, protocol: tcp }]
        env_keys: [DATABASE_URL, SENTRY_DSN, STRIPE_KEY]
        shared_refs: { DATABASE_URL: [DB_USER, DB_PASS] }
        scheduled_tasks:
          - { name: nightly-prune, schedule: "0 3 * * *", command: ["bin/rails", "prune"], enabled: true }
    databases:
      - name: postgres
        engine: postgresql
        version: "16"
        initial_database: acme
        internal_host: cypher-db-db_…      # §3.3
        panel_volume: cypher-db-db_…
        expose_port: null
    compose_stacks:
      - { name: plausible, file: production/stacks/plausible.yml, route: {…}, env_keys: [BASE_URL] }
shared_variables:
  - { key: DB_USER, scope: project }
  - { key: DB_PASS, scope: environment, environment: production }
```

Three things in there are worth defending.

**`deployed_commit`, but the compose file pins the branch.** Knowing which
commit was serving is the single most useful fact for someone rebuilding, so
the manifest records it. The compose `build.context` still pins the *branch*,
because a git remote context resolved to a bare commit sha requires the server
to allow fetching an unreachable object, which GitHub and most hosts do not by
default. A file that pins a sha would work on the maintainer's laptop and fail
on half the hosts that receive it.

**`shared_refs` costs nothing to produce and is not a secret.**
[shared-variables.md](shared-variables.md) §2 stores, in cleartext on
`app_env_vars`, which shared keys each sealed value interpolates — precisely so
no read path has to unseal anything to answer "who uses this". The exporter
reads that column. So the archive can say *"`DATABASE_URL` is built from
`{{shared.DB_USER}}` and `{{shared.DB_PASS}}`"* without ever touching a
ciphertext, and the operator refilling the file knows the shape of the value
they have to reconstruct.

**No `exported_at`.** A wall-clock stamp inside the archive would make every
export differ from every other one, which destroys the only cheap way to answer
*"has anything actually changed since last month?"* — see §5. The instant lives
in the filename and in the HTTP `Date` header, where it does not contaminate
the bytes.

### 3.2 `docker-compose.yml` — the portable half

One file per Environment, holding the Applications and Managed Databases of
that Environment as compose services named after the Resource. Every service
gets `restart: unless-stopped` and an `env_file` (§4).

An Application with `source.kind = image` renders as `image:` — it is already
portable. An Application built from git renders as a remote build context:

```yaml
  api:
    # Built by CypherPanel from 4f2c1ab on branch main.
    build:
      context: "https://github.com/acme/api.git#main"
      dockerfile: ./Dockerfile
    env_file: ["./env/api.env"]
    volumes: ["api-uploads:/app/uploads"]
    ports: ["8443:8443"]
    healthcheck:
      # CypherPanel probed GET /healthz from outside the container; compose has
      # no equivalent, so this needs wget or curl in the image.
      test: ["CMD-SHELL", "wget -qO- http://localhost:8080/healthz >/dev/null 2>&1 || curl -fsS http://localhost:8080/healthz >/dev/null 2>&1"]
      interval: 10s
      timeout: 5s
      retries: 3
```

Four deliberate omissions and one substitution:

- **Nixpacks and Railpack builds cannot be rendered.** A pack build is
  `nixpacks build` writing a Dockerfile, or `railpack prepare` writing a
  BuildKit frontend plan ([pack-builds.md](pack-builds.md)); compose can do
  neither. Those applications render with a `build.context` and a comment
  naming the exact command that produced the Dockerfile, so the operator can
  run one command and then `docker compose build`. Pretending otherwise —
  emitting a `dockerfile: ./Dockerfile` that is not in the repository — would
  produce a file that fails at build time with a message about a missing file
  rather than about a missing step.
- **`replicas` is recorded in the manifest and not rendered.** Compose's
  `deploy.replicas` is Swarm-only and silently ignored under
  [ADR-006](../adrs/ADR-006-docker-only-at-launch.md)'s docker driver — the
  same call [compose-stacks.md](compose-stacks.md) §8 already made about
  `deploy:` keys. The README names `--scale` instead.
- **A `healthcheck` is emitted only for `health.kind = http`.** A `tcp` or
  `none` health check has no portable in-container equivalent, and inventing a
  shell probe for a distroless image produces a service that is permanently
  unhealthy — which under `docker compose up --wait` means a stack that never
  comes up. Those applications get a comment saying what the panel did instead.
- **No `container_name:`.** Same reason compose-stacks.md §3 refuses it on the
  way in: it is absolute, and two environments of one project on one host would
  collide.
- **Volumes are substituted, not copied.** The compose file declares short
  named volumes (`api-uploads`) that compose namespaces under its own project,
  while the manifest records the panel's own deterministic volume name
  (`panel_volume`). That pairing is what an operator needs to `docker run
  --rm -v old:/from -v new:/to …` the data across by hand, and it keeps the
  compose file from claiming ownership of volumes on a host where they do not
  exist.

### 3.3 Managed Databases, and the alias that makes connection strings survive

A Managed Database renders as its engine image with the engine matrix's own
data path and health command — `domain.EngineDefaults` is the single source for
both, so the exported health check is literally the one the panel uses:

```yaml
  postgres:
    image: postgres:16
    env_file: ["./env/postgres.env"]
    volumes: ["postgres-data:/var/lib/postgresql/data"]
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U postgres"], interval: 10s, timeout: 5s, retries: 5 }
    networks:
      default:
        # CypherPanel's internal hostname for this database. Kept so that a
        # connection string copied from the old panel still resolves.
        aliases: ["cypher-db-db_7a1c…"]
```

The alias is not decoration. The panel's in-network hostname for a database is
`cypher-db-<id>` — that is what `GET /databases/{id}/connection-info` returns
and what `core/templates` bakes into every generated connection string for the
137 bundled entries. Without the alias, every `DATABASE_URL` an operator pastes
back into the exported stack would point at a host that does not exist, and the
failure would look like a database problem rather than a rename.

**Applications get no such alias, and the archive does not pretend otherwise.**
An application's container name carries its revision id
(`cypher-<app>-<revision>`), so it changes on every deploy and nothing can
depend on it today; app-to-app service discovery is a gap this feature reports
rather than closes (§11).

### 3.4 Compose Stacks are copied, never merged

A Compose Stack's file already *is* the desired state
([compose-stacks.md](compose-stacks.md) §1). It is written to
`<environment>/stacks/<name>.yml` **verbatim**, exactly as the operator wrote
it, and the README gives the command to run it. It is not merged into the
environment's `docker-compose.yml`: merging means resolving service-name
collisions and reconciling two files' network assumptions, and rewriting an
operator's own file is the one thing the Compose Stack feature exists not to
do.

One consequence must be stated rather than discovered: a stack file may contain
an inline secret the operator put there by hand. The panel cannot tell the
difference between that and any other string, so it travels. This is the same
disclosure compose-stacks.md §7 already accepts (it is why the file is
admin-rank to write and why the audit log records that the file changed, never
its content), and it is why the export is admin-rank to take (§6).

### 3.5 Routing: no Traefik labels

Domains are recorded in the manifest and named in the README, and the compose
file publishes only the host ports the Resource actually declared. It emits no
Traefik labels, and this is the decision most likely to be questioned, so:

Traefik labels are read by Traefik's **docker provider**, which requires
mounting the Docker socket into Traefik.
[ADR-004](../adrs/ADR-004-traefik-file-provider.md) refuses that arrangement
for the managed Proxy — *"exposing the Traefik API for dynamic config …
unnecessary attack surface"* — and an export that ships labels would be the
panel teaching, on the way out, the exact configuration it declines to run.
Two lesser reasons make it easy: the labels would encode *our* entrypoint and
`certResolver` names, which mean nothing on the recipient's host, and we could
never test them, because the recipient's proxy is not ours to boot.

What ships instead is honest: the manifest's `route` block, and a README
section listing each domain, the service that answers it and the port, ready to
paste into whatever proxy the operator already runs. TLS is theirs to arrange —
the panel's ACME account is panel-wide
([agent-identity-and-tls.md](agent-identity-and-tls.md)) and is not exportable
material.

### 3.6 Scheduled tasks

Scheduled tasks are recorded in the manifest and echoed as a comment block
above the service they belong to. Compose has no cron, and the honest options
were: emit nothing, or emit a sidecar (`ofelia`, `mcuadros/ofelia`, a busybox
crond) that the panel would then have chosen on the operator's behalf, pinned a
version of, and be quietly responsible for. [ADR-011](../adrs/ADR-011-in-container-scheduled-tasks.md)
put the clock in the agent precisely so no third-party scheduler is in the
product's supply chain; an export is not the place to add one. The comment
carries the cron expression and the argv, which is what a `crontab` or a
systemd timer needs.

## 4. Secrets: keys, never values — and a check that is structural

**Values are not exported. Ever.** Application environment variables, Compose
Stack environment variables, shared variables, database root passwords, webhook
HMAC secrets, deploy-key private halves, registry credentials, DNS and SMTP
tokens: none of them appear in any file in the archive, in any form, including
a masked hint (ENGINEERING rule 20, and ui-principles §6's write-only rule).

Each Resource gets `env/<name>.env.example`:

```
# CypherPanel exported the KEYS of this resource's environment. Values stay
# sealed in the panel and were not exported. Fill them in and rename this file
# to api.env.
DATABASE_URL=      # built from {{shared.DB_USER}}, {{shared.DB_PASS}}
SENTRY_DSN=
STRIPE_KEY=
```

**The compose file references `./env/api.env`, which the archive does not
contain.** So `docker compose up` on an untouched archive fails immediately
with `env file ./env/api.env not found` — naming the file, which is the
remedy. That is ui-principles §1's error rule applied to a file someone will
read on another machine three weeks from now: the alternative, shipping
`api.env` with empty values, starts the stack and lets each service crash for
its own unrelated-looking reason.

A database's `env/<name>.env.example` carries the same treatment for the
engine's password variable, plus the one sentence an operator needs and will
not otherwise expect: a new password does not open an existing data directory,
so a rebuilt database starts empty or is restored from a Database Backup.

**The check that makes this structural rather than a promise.** `core/export`'s
consumer-defined `Store` interface (ENGINEERING rule 6) has no method that
returns a ciphertext and no dependency on the `secret.Box` `Opener`. The
package *cannot* unseal a value; there is nothing in scope to unseal it with.
This is why the exporter deliberately does **not** reuse the scheduler's
`buildSpec`, which would have been the obvious code reuse: `buildSpec` unseals
every env var and expands shared variables, because an `AppSpec` on the wire to
an agent carries plaintext env by design (`work.proto`). Reusing it would have
put every secret in the project one serialization mistake away from a download.
`TestExporterHasNoOpener` is a compile-shaped assertion of that; the behavioural
one is acceptance test 3.

## 5. Determinism and streaming

**Two exports of an unchanged project are byte-identical.** Tar entries are
emitted in a fixed order with zeroed mtime, uid/gid and uname/gname; gzip is
written at a fixed level with a zeroed header mtime; map-shaped YAML is emitted
with sorted keys. Nothing inside the archive carries a clock (§3.1).

That property is cheap and it buys something real: `sha256sum` on two archives
answers "did anything change" without a diff tool, and an operator who commits
their export to git gets a history whose diffs are only ever actual
configuration changes. It also makes the golden-file tests in `core/export`
possible at all.

**The archive is streamed, never buffered and never spooled to disk.** The
handler writes `gzip.Writer` → `tar.Writer` straight onto the
`http.ResponseWriter`, one bounded file at a time.
[vision.md](../vision.md)'s first non-negotiable is a control plane under
300 MB RSS, and a feature whose memory cost is "one operator's largest project"
is a feature that decides that budget for everyone. There is no temporary file
on the plane, which is the same rule [ADR-008](../adrs/ADR-008-no-registry-required.md)
holds for relayed images.

Streaming has one real cost and it is paid for explicitly: **once the first
byte is written, the status code is spent.** So every operation that can fail —
authorization, resolving the project, reading every row — completes *before* the
tar writer is created. The write phase can then only fail on a client that has
gone away, and a client that has gone away needs no status code. If a write
does fail, the archive is truncated, and a truncated gzip fails its own CRC and
length trailer — so a partial download is refused loudly by `tar` rather than
opening as a smaller, plausible-looking, wrong archive.

## 6. Authorization and audit

| Route | Rank | Ability | Notes |
|---|---|---|---|
| `GET /api/v1/projects/{id}/export` | **team admin** | `read` | `application/gzip` |
| `GET /api/v1/auth/me/deletion-preview` | own account | — | session only |
| `POST /api/v1/auth/me/delete` | own account | — | session only |

**Export is team admin, and it is worth being honest about what that gate does
and does not buy.** It is not a confidentiality boundary: a team member can
already read every field the archive contains, one route at a time, and could
assemble an equivalent archive with a shell script. Claiming the rank as a
security control would be a lie of the kind ENGINEERING rule 20 exists to
prevent.

What the rank buys is that the *aggregated, one-click, leaves-the-panel* form
of that read sits at the rank that manages the project — the same rank that can
delete it, and the same rank that may write a Compose Stack file, which the
archive carries verbatim (§3.4). An API token may take an export at `read`
ability, because a nightly `export → git commit` is a legitimate and rather
good use of this feature, and refusing it would only push operators into a
scraper that is worse in every way.

**`project.exported` joins the audit vocabulary** (`core/audit/actions.go`,
`project` family), with the counts of what was exported in `detail` and — per
audit-log.md §6 — no content whatsoever.

This is the **first audited read** in the log, whose vocabulary has so far been
entirely mutations, so the precedent needs bounding rather than setting by
accident. The rule it establishes is narrow: *a read is audited when it produces
a durable artifact outside the panel.* An export does; `GET /applications/{id}`
does not, and neither does anything else today. This does not open the door to
auditing every GET — that would be an access log, which audit-log.md §9
explicitly rejected building.

## 7. Account deletion: what it refuses

`POST /api/v1/auth/me/delete` deletes the caller's own account. It is
**session-only** — an API token inherits its owner's role, and a leaked CI
credential must not be able to delete the person it belongs to. It requires the
**current password** in the body, the same proof `POST /auth/password` demands
for the strictly less destructive act of changing it, and the UI additionally
requires typing the account's email address (ui-principles §2: an irreversible
action requires typing the resource's name, and the account's name is its
address).

It refuses in exactly two cases, both 409:

1. **You are the last owner of a team.** The canvas copy: *"Blocked while
   you're the last owner of any team — transfer ownership first."* The refusal
   **names the teams**, the way a refused registry delete names the
   applications ([registries.md](registries.md) §7) — a 409 that says
   "something is in the way" without saying what is a dead end
   (ui-principles §11).
2. **You are the last panel owner.** This one is *not* on the design screen and
   is not optional: `teams.guardLastPanelOwner` already refuses a self-demotion
   that would leave the panel ownerless, and self-deletion is a strictly more
   effective way to do the same thing. Shipping deletion without it would make
   the account settings page a supported route around an existing invariant.

The last-team-owner guard counts **membership rows**, exactly as
`teams.guardLastOwner` does today — the panel-owner bypass
([teams-and-roles.md](teams-and-roles.md) §1) does not count as an owner of a
team it holds no membership in. The two must use one predicate, because a guard
that answers differently depending on which route asks is not a guard. The
recovery path the refusal points at is the team's members screen, where another
member is promoted; a team with no one else to promote is deleted instead,
which is itself refused while it owns projects (`ErrTeamNotEmpty`) — a chain
where every step names its own next step.

**Deleting an account deletes no Project, Environment, Application, Managed
Database or Compose Stack.** Resources belong to teams. The confirmation says
that in as many words, because the opposite assumption is the one a person
about to click has, and it is the assumption that makes them not click.

## 8. What deletion takes, and what it leaves

No migration and no new column: every `users(id)` reference in the schema is
already `ON DELETE CASCADE` or `ON DELETE SET NULL`, and the cascade set is
exactly right. What the operator must be told is the *blast radius*, which is
what `GET /auth/me/deletion-preview` returns and the confirmation renders:

| Removed with the account | Where |
|---|---|
| Sessions (including the one making the request) | `sessions` CASCADE |
| **API tokens** — CI that uses one stops working | `api_tokens` CASCADE |
| Two-factor secret and recovery codes | `users` row, `totp_recovery_codes` CASCADE |
| Profile photo, inbox items and preferences | `user_avatars`, `inbox_items`, `inbox_preferences` CASCADE |
| A pending address move | `email_changes` CASCADE |
| Team memberships | `team_members` CASCADE |
| Pending access requests they raised | `access_requests` CASCADE |
| Live invitations they issued | explicit revoke, below |

The API-token row is the one that surprises people, so it is a counted line in
the confirmation — *"3 API tokens stop working; deploys that use them will
fail"* — not a footnote.

**Invitations they issued are revoked**, not orphaned.
[invitations-and-access-requests.md](invitations-and-access-requests.md) §8
already holds that *"an invitation outlives its issuer's session, but not their
membership"*, and `RevokeLiveTeamInvitesBy` implements it for one team on member
removal. Deleting an account is the same rule across every team at once, so the
store gains one method (`RevokeLiveInvitesBy(userID)`) and the `invited_by`
column's `ON DELETE SET NULL` stops being the thing that decides an
invitation's fate.

**What is left behind is left deliberately.** `deploy_approvals.decided_by`,
`break_glass_grants.opened_by` and `access_requests.decided_by` are `ON DELETE
SET NULL`: those rows keep their decision and lose their live pointer. That is
correct and needs no fixing here, because they were never the evidence —
audit-log.md §1 moved that job to a table with no foreign keys at all, and
`protection.approved` still names the approver by the label snapshot taken when
they approved.

## 9. "Audit entries stay, attributed to a deleted user"

The canvas sentence is the whole requirement, and it strains a decision
audit-log.md already made, so it gets resolved in writing rather than in a
handler.

audit-log.md §1 says an entry *"must not be rewritten"* — names are snapshots,
copied at write time, and *"the only mutation the table ever sees is the
retention purge"*. §12 puts *"redacting a deleted account's label from
historical entries"* explicitly out of scope. Read literally, "attributed to a
deleted user" could mean rewriting `actor_label` to `deleted user` on every
row the account ever produced — which is a bulk UPDATE of an immutable ledger,
and it would destroy the evidentiary value of exactly the rows most worth
keeping (*who* deleted the production database, three weeks before they closed
their account).

**The resolution is that attribution changes, not the row.** The entry keeps
its `actor_label` (the address as it read at the time) and its
`actor_user_id`, both of which are already FK-free snapshots and therefore
survive the delete untouched. The `AuditEvent` DTO gains one additive boolean,
`actor.account_deleted`, and the UI renders *"priya@acme.com · deleted
account"*. The design's sentence is honoured — the entries stay, and they are
visibly attributed to a deleted user — without the log becoming writable.

Computing that flag costs one `LEFT JOIN users` applied to the **page**, in the
outer select over rows the cursor has already limited to at most 200 — never
inside the `visible` CTE, whose `NOT MATERIALIZED` plan audit-log.md §2 went to
some trouble to earn (152 ms → 0.14 ms on 200k rows). A join over ≤200 primary
keys does not move that number.

Two smaller consequences:

- **The last thing an account does is record its own deletion.** The handler
  writes `user.deleted` after the row is gone, with the actor snapshot taken
  from the request's user value. No foreign key means it lands cleanly, and the
  row that survives an account is the one describing its end.
- **Each team gets its own entry.** The `user.deleted` row is panel-scoped
  (`team_id` NULL), so under audit-log.md §5 only panel admins can read it —
  which would mean a member vanishing from a team with nothing in *that team's*
  timeline to say so. So deletion also writes one `team.member_removed` per
  team the account belonged to, with `detail.reason: "account deleted"`. Both
  verbs already exist; the vocabulary gains only `project.exported`.

**Erasure is not offered, and the reason is recorded.** The addresses in the
log are the point of the log, and audit-log.md §6 already named the panel's
erasure horizon: `CYPHERD_AUDIT_RETENTION`, 90 days by default. An operator who
needs a shorter one sets it. A per-account erasure verb is a compliance feature,
and compliance regimes are on [vision.md](../vision.md)'s anti-persona list;
building one here by the side door would be the wrong way to change that.

## 10. API surface and UI

Three operations, taking the contract from 198 to 201. No migration, no proto
change, no NATS subject, no agent code — every byte of an export and every
guard on a deletion is control-plane state the plane already holds.

```
GET  /api/v1/projects/{id}/export        → 200 application/gzip  (team admin, read)
GET  /api/v1/auth/me/deletion-preview    → 200 AccountDeletionPreview   (session)
POST /api/v1/auth/me/delete              → 204 | 409 LastOwnerConflict  (session)
```

`AccountDeletionPreview` is
`{blocking_teams: [{id, name}], teams, api_tokens, sessions, pending_invitations}`
— counts and blockers, nothing else. It reads and changes nothing, so the
dialog may call it every time it opens, exactly like
`GET /panel/dns/disconnect-preview`.

**UI: one new tab, `Settings → Export`** (`web/src/routes/_app/settings/export.tsx`),
holding both rows of canvas 14k. That makes thirteen tabs in a strip whose own
comment already calls twelve *"more than a phone can hold"* — a real cost,
accepted because the folding "More" menu absorbs it and because splitting the
pair defeats the screen (§1). The copy is the canvas copy.

The download cannot be an `<a href download>`: session auth is a bearer token
in a header (`web/src/api/client.ts`), and putting one in a query string writes
the credential into browser history and every log between here and there — the
comment on `apiBlob`, which exists for exactly this reason on avatars, says so.
So the archive is fetched through `apiBlob`, handed to an object URL and
clicked synthetically, with the URL revoked afterwards. The button shows
progress past 300 ms (ui-principles §3) and its failure state names the project.

Deletion redirects to the sign-in screen with *"Your account has been
deleted."* — the caller's own session dies with the row, so any other outcome
would be a client watching its own requests 401 with no explanation.

## 11. Acceptance (testable)

1. Export a project with one git-built Application, one image Application, one
   Managed Database, one Compose Stack and two Environments → the archive
   contains one directory per non-preview environment, a manifest, an
   `env/*.env.example` per resource, and the stack file byte-identical to
   `GET /compose-stacks/{id}/file`. (`TestExportArchiveLayout`,
   `TestStackFileIsCopiedVerbatim`)
2. A preview environment on that project appears nowhere in the archive.
   (`TestPreviewEnvironmentsAreNotExported`)
3. Set `STRIPE_KEY=sk_live_hunter2` and a database root password, export, and
   grep every byte of the decompressed archive: neither value appears, the key
   `STRIPE_KEY` does, and `DATABASE_URL`'s example line names its two
   `shared.` references. (`TestExportCarriesKeysNeverValues`)
4. Two exports of an unchanged project are byte-identical; changing one
   application's port changes the bytes. (`TestExportIsDeterministic`)
5. **Integration (real Docker):** export a project holding an `nginx` image
   Application and a PostgreSQL Managed Database, fill in the env files,
   `docker compose up --wait` in the exported directory → both services report
   healthy, and the database is reachable at the aliased hostname
   `cypher-db-<id>`. (CI job `export`, `TestExportedArchiveBoots`)
6. A team member is refused the export (403); a team admin is allowed; a member
   of another team gets 404. An API token with `read` succeeds; the same token
   scoped to a different project does not.
   (`TestExportRequiresTeamAdmin`, `TestExportScope`)
7. Taking an export writes one `project.exported` audit row with counts and no
   content; no other GET in the API writes one.
   (`TestExportIsAudited`, `TestNoOtherReadIsAudited`)
8. Deleting an account while the last owner of one team → 409 naming that team;
   after a second owner is added → 204. The last panel owner is refused even
   with a co-owner on every team. (`TestDeleteMeRefusesLastTeamOwner`,
   `TestDeleteMeRefusesLastPanelOwner`)
9. Deletion with a wrong password → 401, account intact; with an API token
   instead of a session → 403. (`TestDeleteMeProvesThePassword`,
   `TestDeleteMeIsSessionOnly`)
10. After deletion (real Postgres): sessions, tokens, TOTP, avatar, inbox items
    and memberships are gone; the account's live invitations are revoked; the
    projects of its teams are untouched.
    (`TestStoreAccountDeletionCascade`, `TestDeletionRevokesIssuedInvitations`)
11. Every audit entry the account produced is still readable, still labelled
    with its address, still scoped to the team it happened in, and now carries
    `actor.account_deleted: true`; a `team.member_removed` row is readable by
    each team it left. (`TestAuditEntriesOutliveTheirAccount`,
    `TestDeletedAccountIsMarkedOnItsEntries`)
12. The exporter package cannot unseal: `core/export` compiles with no
    `secret.Opener` in its dependency graph. (`TestExporterHasNoOpener`)

## 12. Deliberately out of scope

- **Import.** Nothing reads an archive back into CypherPanel. `export_version`
  exists so a future importer has a contract to read, and that is all it does
  today. Round-tripping is the same problem as the **Migration importer from
  Coolify / Dokploy** row (feature-matrix V1.x) and deserves that row's design,
  not a corner of this one.
- **Volume and database contents.** §2. Database data has a feature
  ([managed-databases.md](managed-databases.md) §7); application volume data
  has none, and inventing one inside a download would be the worst possible
  place for it.
- **Panel-wide or team-wide export.** One project per archive. A panel export
  is a different artifact with a different security posture — it would contain
  every team's configuration in one file — and it needs its own argument.
- **Exporting the audit log.** Already out of scope in audit-log.md §12
  (CSV/JSON download, SIEM shipping), and still is. The log is not project
  state.
- **Exporting panel-level configuration** — servers, deploy keys, backup
  targets, registries, DNS, mail, TLS. They are not in a project, most of them
  are credentials, and a "settings export" is a much narrower conversation
  about which of them are safe to write down.
- **Signed or encrypted archives.** The transport is already authenticated
  mTLS-fronted HTTPS and the archive holds no secret value, so signing would
  protect bytes that need no protection. If volume data is ever added, this
  becomes a prerequisite rather than a nicety.
- **App-to-app service discovery.** §3.3 reports the asymmetry — databases have
  a stable internal hostname, applications do not — and does not fix it. Giving
  application containers a stable network alias changes the reconciler and
  belongs to its own spec.
- **Deleting an account that is the last owner of a team, by cascading the
  team.** The refusal is the feature. A delete that quietly took a team and its
  projects with it is precisely the outcome §7's confirmation copy exists to
  rule out.
- **GDPR-style erasure of a deleted account's audit labels.** §9, with the
  reason.
- **Scheduling an export** (nightly to S3, or to a Backup Target). An operator
  can already loop the API with a `read` token, which is the honest amount of
  machinery this deserves until someone asks twice.
