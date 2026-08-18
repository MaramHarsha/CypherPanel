# Feature spec: Template catalog

> Phase 4's headline ([roadmap.md](../roadmap.md)), governed by
> [ADR-007](../adrs/ADR-007-template-format.md): a **native declarative template
> schema** that resolves to ordinary Applications and Managed Databases, a
> **Coolify importer** that translates their compose+magic-env library into the
> native schema, and a catalog **bundled in the release**. Written 2026-08-02,
> just before implementation (CLAUDE.md rule 7). Vocabulary per
> [glossary.md](../glossary.md).

## 1. What a template is

A template is a YAML document declaring the CypherPanel resources one click
creates: image-based Applications (source.kind=`image` — the deploy-from-image
pipeline) and Managed Databases, wired together with generated secrets and
connection references. Installing resolves it into ordinary rows — after
install, a templated app is indistinguishable from a hand-made one: same
pipeline, same rollback, same backups, same danger zone (ADR-007 §Decision 1).
There is no template runtime and no second code path.

Templates are **content, not code**: no shell strings, no arbitrary compose
graphs, no host mounts. Anything the schema cannot express is rejected at
load time, not half-installed (ADR-007 §Decision 2 applies the same rule to
imports).

## 2. Schema (v1)

```yaml
schema: v1                    # literal; unknown values rejected
slug: n8n                     # [a-z0-9-], unique in the catalog, ≤40 chars
name: n8n
description: Workflow automation with a visual editor.   # ≤200 chars
category: automation          # one of: ai · analytics · automation · cms
                              #   · communication · dev-tools · finance · media
                              #   · monitoring · productivity · security
                              #   · storage · other
version: "1.94"               # the packaged upstream version (display only)
resources:
  databases:                  # optional, ≤3
    - name: db                # [a-z0-9-], unique within the template
      engine: postgresql      # the six managed engines (managed-databases.md)
      version: "16"           # optional; engine default when omitted
  applications:               # ≥1, ≤5
    - name: n8n
      image: docker.n8n.io/n8nio/n8n:1.94.1   # digest-pinned or tagged OCI ref
      port: 5678              # container port
      route: true             # true = takes the install-time domain (≤1 app
                              #   per template may set it)
      health:                 # optional; defaults kind=http path=/
        kind: http            # http | tcp | none
        path: /healthz
      volumes:                # optional, ≤5: name + absolute path
        - name: data
          path: /home/node/.n8n
      ports: []               # optional raw host publishes {host, container,
                              #   protocol} — for non-HTTP services (minio API)
      env:                    # optional; values are literals or placeholders
        DB_POSTGRESDB_HOST: "{{db.db.host}}"
        DB_POSTGRESDB_PASSWORD: "{{db.db.password}}"
        N8N_ENCRYPTION_KEY: "{{secret.32}}"
        WEBHOOK_URL: "https://{{domain}}/"
```

### Placeholders

The only dynamic values, resolved once at install time by a strict tokenizer
(no general templating engine — a malformed token is a load-time error):

| Token | Resolves to |
|---|---|
| `{{db.<name>.host}}` | the database container's DNS name on the shared environment network (`cypher-db-<id>`) |
| `{{db.<name>.port}}` | the engine's canonical port |
| `{{db.<name>.user}}` | the engine's root user |
| `{{db.<name>.password}}` | the generated root password (sealed once it lands in the app's env) |
| `{{db.<name>.database}}` | the application database the install asked the engine to create (managed-databases.md §2); the engine default when it asked for none |
| `{{db.<name>.url}}` | engine URL (`postgres://user:pass@host:port/db`, `redis://:pass@host:port`, …) |
| `{{secret.N}}` | a fresh random secret of N bytes hex-encoded (16 ≤ N ≤ 64), generated per install |
| `{{domain}}` | the domain the operator entered at install (empty when none) |

A template referencing `{{db.x…}}` must declare database `x`. Engines without
passwords (redis/valkey default) resolve `password` to the generated one —
template databases always opt into `require_password` so references never
resolve empty.

**Deliberately absent:** operator-defined free-form inputs. Everything a v1
template needs is a domain + generated values; an `inputs:` section is a
follow-up once a real template demands one, not speculative surface.

## 3. Catalog packaging

Templates live in `core/templates/catalog/*.yaml` and are embedded
(`go:embed`) — versioned with the binary, no runtime fetch, no network
dependency (ADR-007 §Decision 3). A unit test parses and validates **every**
bundled file; an invalid template cannot ship.

The catalog is a hand-curated core plus everything §6's importer converts:
single-container tools and stacks backed by any of the managed engines.
MySQL- and MariaDB-backed stacks (WordPress, Ghost) arrived with the
Managed-Database initial-database field
([managed-databases.md](managed-databases.md) §2), which every install now
uses so that `{{db.<name>.database}}` names a database that exists.

## 4. Install semantics

`POST /api/v1/templates/{slug}/install` `{environment_id, server_id, domain?,
name?}` → `202 {applications: [ids], databases: [ids]}`.

Order: databases first — each created with an application database of its own,
derived from the install name, which is what makes `{{db.<name>.database}}`
resolve to something that exists (their `Create` returns the root password
exactly once — captured only to resolve placeholders, then discarded; the
sealed copy in the app's env vars is the durable one) — then applications
(source.kind=`image`, env vars sealed by the applications service), then one
deploy per application through the ordinary scheduler (image deploys go
straight to rollout). Resource names are `<name>-<resource>` where `name`
defaults to the slug; a name collision in the environment fails validation
before anything is created.

**Ordering, and its current limit.** Databases are created before the
applications that reference them, but `Create` returns once provisioning work
is *published*, not once the engine accepts connections — so a dependent app
can start while its database is still coming up. Its first health gate is
therefore given a deliberately patient budget (10 s × 18 ≈ 3 minutes) whenever
the template declares a database, which covers ordinary provisioning on modest
hardware.

That is a mitigation, not an ordering guarantee. Making the deploy genuinely
wait belongs in the scheduler as desired state — "this revision is deployable
once these databases report running" — rather than as a sleep in the install
path, which would not survive a plane restart. Recorded as follow-up; until
then a database that takes longer than the gate leaves a failed first
deployment that redeploys cleanly once it is up.

**Failure:** install is not transactional across resources. On a mid-install
error the service best-effort deletes what it created in this call (reverse
order), then reports the underlying error; a deletion that itself fails is
reported alongside so the operator sees exactly what exists. Nothing is ever
retried implicitly.

**Authz:** same as creating the resources by hand — `RoleMember` on the
project owning the environment. Listing the catalog requires only a session
(it is static content).

## 5. API surface

```
GET  /api/v1/templates                → [{slug, name, description, category, version, resources summary}]
GET  /api/v1/templates/{slug}         → full template (resolved schema, placeholders visible)
POST /api/v1/templates/{slug}/install → 202 {applications, databases}
```

Spec-first in `openapi.yaml`; orval regenerates the client; the templates UI
page (today an empty state) becomes: category-grouped catalog with search →
detail pane (what it creates, env keys, volumes) → install dialog
(environment, server, domain) → navigate to the created application.

## 6. The Coolify importer

`core/cmd/coolify-import` — a **build-time tool**, not a runtime code path
(ADR-007 §Decision 2): reads Coolify compose templates
(`coolify/templates/compose/*.yaml`, read-only reference), emits native
template YAML, or **rejects loudly** with every reason listed. Its output and
its refusals are documented in [dev/template-import.md](../dev/template-import.md).
Mapping:

- one compose service with an `image` → an application; `SERVICE_FQDN_<name>`
  → `route: true` + `{{domain}}` references; `SERVICE_PASSWORD_<name>` /
  `SERVICE_BASE64_*` → `{{secret.N}}` of matching length; volumes → named
  volumes (host-path mounts rejected); `environment` literals carried as-is
  after compose's `${VAR:-default}` interpolation is resolved.
- a service whose image matches a managed engine (postgres/mysql/mariadb/
  mongo/redis/valkey families) → a managed database, its consumers rewired to
  `{{db.<name>.*}}` references — including the DSN's host and, for PostgreSQL,
  its database segment. Cache URLs gain the credentials the managed engine
  requires, since template databases always demand a password.
- rejected (with reasons): `build:`, `command:`/`entrypoint:` overrides,
  host-path or file mounts, `cap_add`/`privileged`/`devices`, custom
  networks, `depends_on` graphs deeper than app→db, more than one
  FQDN-routed service, engines outside the matrix, one-shot job containers,
  a generated value used twice (`{{secret.N}}` resolves per occurrence, so
  the copies would differ), and any application addressed by hostname —
  application containers are named per revision and have no stable address.

Imported files are reviewed and committed like hand-written ones — the
importer widens the funnel; the catalog test is the gate. Per-directory
license checks apply before reading any Dokploy path (CLAUDE.md rule 1);
the importer reads only Coolify's Apache-licensed template files, and the
translated output contains configuration facts, not copied code.

**No bundled template declares host ports.** An app publishing fixed host
ports cannot currently be redeployed (see the known limitation in
[application-deploy.md](application-deploy.md) §5), and a catalog entry that
installs but can never be updated is worse than an absent one. Syncthing was
dropped from the launch set for exactly this reason; it returns when that
limitation is resolved.

**Image tags are pinned, never floating.** A bundled template must resolve to
the same version on every panel running a given CypherPanel release — and
mutable tags are re-pulled on every deploy, so `:latest` would let a routine
redeploy cross a major version unannounced.

## 7. Non-goals (this slice)

Remote template index (ADR-007 records it as a later escape hatch) ·
Compose Stack resources (separate Phase 4 feature; templates that need
arbitrary graphs wait for it) · operator inputs beyond domain/name ·
per-template upgrade flows (a templated app updates like any image app:
edit the image, deploy) · MySQL/MariaDB-backed catalog entries (named-db
follow-up above).

## 8. Acceptance

- Every bundled template validates in CI; installing any of them on a live
  panel yields running, routed, health-gated resources with sealed secrets,
  drivable purely via REST.
- Deleting the created resources leaves nothing behind (ordinary deletes).
- *(When the importer lands)* it converts at least one real Coolify template
  end-to-end and rejects one that needs a Compose Stack, with actionable
  reasons.
- `.claude/skills/adding-a-template` exists (project-structure.md plans it)
  and walking it produces a template that passes the catalog test.
