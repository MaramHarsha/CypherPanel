# Importing Coolify's template library

> How `core/cmd/coolify-import` turns Coolify's compose templates into catalog
> entries, what it refuses, and how to re-run it. The schema and install
> semantics live in [features/template-catalog.md](../features/template-catalog.md);
> this is the tooling doc.

## Why a build-time tool

The catalog is the one part of a PaaS that is pure breadth: nobody can
hand-write two hundred correct application configurations, and the reference
projects already did that work. Coolify's `templates/compose/*.yaml` are
Apache-2.0 **configuration data**, and porting configuration facts is exactly
what [research/coolify.md](../../research/coolify.md) marks them for.

What we do not port is the runtime. Coolify installs a template by handing
arbitrary compose to a Docker daemon; CypherPanel resolves one into ordinary
Applications and Managed Databases and then forgets it was a template
(ADR-007). So the translation happens **once, offline, where a human reads the
result** — never in the panel. The importer emits YAML that is reviewed and
committed like a hand-written template, and the catalog unit test is the gate
that decides whether it ships.

That is also why the importer is allowed to be strict. It has no obligation to
convert everything; it has an obligation never to produce a template that
installs and then does not work.

The per-template verdicts — every refusal, with its reasons — are regenerated
into [template-import-report.md](template-import-report.md) on each run, so
"why isn't X in the catalog?" has an answer that cannot drift from the catalog
it describes.

## Running it

`make templates-import` is the whole flow: it re-imports, refreshes the report,
and runs the catalog test. The underlying tool, if you want to drive it
directly:

```sh
# Report only — offline, writes nothing. Good for reviewing mapping changes.
go run ./cmd/coolify-import -report /tmp/report.md ../coolify/templates/compose/*.yaml

# Produce the catalog. -pin resolves every image against its registry.
go run ./cmd/coolify-import -pin -cache /tmp/images.json \
    -out templates/catalog -report /tmp/report.md \
    ../coolify/templates/compose/*.yaml
```

Run either from `core/`. `-cache` is worth passing every time: anonymous registry
quotas are the slowest part of a run, and the cache makes a re-run after a
mapping change cost no requests at all. Delete it to re-resolve from scratch —
which is what a deliberate catalog refresh wants, since the point of re-running
is to pick up new upstream releases.

`-out` rewrites only files it wrote before — every generated template carries a
marker comment on its first line, and a catalog entry without one is never
touched. Hand-written and imported templates therefore share one directory
without fighting, and a curated entry (a real health path, a description
someone wrote) always outranks the generated version of the same slug; the
report records where that happened. After any run: `go test ./templates/...`,
then read the diff.

## What the mapping does

| Coolify | Native template |
|---|---|
| service with an `image` | an application |
| service whose image is a managed engine | a managed database, named `db` or `cache` by role |
| `SERVICE_FQDN_<svc>[_<port>]` (bare key) | `route: true` and the port |
| `$SERVICE_FQDN_<svc>` / `$SERVICE_URL_<svc>` | `{{domain}}` / `https://{{domain}}` |
| `$SERVICE_PASSWORD_<name>`, `$SERVICE_BASE64_*`, `$SERVICE_HEX_*` | `{{secret.N}}` of matching length |
| the same, where `<name>` is an engine | `{{db.<n>.password}}` / `{{db.<n>.user}}` |
| a database service's name used as a host | `{{db.<n>.host}}` |
| a DSN's database path segment | `{{db.<n>.database}}` (never for a cache — that path is a database *number*) |
| `${VAR:-default}` | the default, resolved project-wide |
| `${VAR}` with no default anywhere | an empty value the operator fills in after install |
| `healthcheck:` | dropped; the application gets a TCP probe of its own port |
| `restart:`, `container_name:`, `logging:`, `labels:` | dropped; the panel owns all four |

Four of those deserve their reasoning stated, because they are where a
faithful-looking translation would still be wrong:

**Health checks become TCP, not HTTP.** A compose healthcheck is a shell
command, which the schema has no field for. The tempting substitute — an HTTP
probe of `/` — fails every application that answers a redirect to a login page,
and the probe gates the rollout. A TCP connection to the application's own port
is the strongest readiness signal that holds for an image nobody has inspected.

**Cache URLs gain credentials.** Coolify's Redis services usually run without a
password, so their templates write `redis://redis:6379`. Template installs opt
every managed database into password authentication, so that URL is rewritten
to carry `{{db.<n>.password}}`. A template with nowhere to put the password —
one that configures a cache host and port but no credential — is refused
instead, because it would install and then fail to authenticate.

**Database names are rewritten, not carried.** Upstream creates the
application's database through the engine container's `POSTGRES_DB` or
`MYSQL_DATABASE`; a template install creates its own
([managed-databases.md](../features/managed-databases.md) §2). Carrying the
literal would point the application at a database nobody creates, so every
reference resolves through `{{db.<n>.database}}` — and a name that survives
translation anywhere is a refusal, not a warning.

**A scoped database user becomes the root one.** Coolify's MySQL templates
habitually give the application its own user
(`MYSQL_USER=$SERVICE_USER_WORDPRESS`). A managed database creates no such
user, so those references resolve to the root credentials it does have. That is
the same posture every other converted template already has — all of them
connect as `root` or `postgres` — not a new one.

## What it refuses, and why

Refusals are listed in full per template, never one at a time: someone deciding
whether a blocker is worth removing needs the whole story.

**Unsupported by the schema, and deliberately so** — these need a Compose Stack
resource, which is [a separate Phase 4 feature](../features/template-catalog.md#7-non-goals-this-slice):

- `command:` / `entrypoint:` overrides — templates configure through
  environment only.
- host-path and bind mounts — a template may not reach outside panel storage.
- `privileged`, `cap_add`, `devices`, `sysctls`, `security_opt`, `user`,
  `hostname`, `platform`, custom networks, `network_mode`, `links`.
- `ports:` — an application publishing fixed host ports cannot currently be
  redeployed ([application-deploy.md](../features/application-deploy.md) §5),
  and a catalog entry that installs but can never be updated is worse than an
  absent one.
- `build:` — templates install published images.

**Unsupported by the resource model:**

- **Applications addressed by hostname.** Application containers are named per
  revision, so there is no stable DNS name and no placeholder for one. A
  two-application template survives only if the halves do not talk to each
  other directly.
- **One-shot job containers** (`restart: no`/`on-failure`) — usually a
  migration step. The schema has no job resource, and installing one as an
  application means a health gate that can never pass.
- **More than one publicly routed service** — a template takes one domain.

**Unsupported by the placeholder grammar:**

- A generated value used more than once. Coolify generates per *name* and
  substitutes it everywhere; `{{secret.N}}` resolves per *occurrence*, so two
  uses would install as two different secrets — a template that starts and then
  rejects its own credentials.
- `SERVICE_REALBASE64_*` (base64 of N random bytes) and `SERVICE_SUPABASE*`
  (a JWT signed with another generated value). `{{secret.N}}` is hex, which is
  neither.
- A variable with no default interpolated *into* a larger string, which would
  install a truncated URL or DSN that looks configured and is not. A variable
  that is the *whole* value is fine: it installs empty and is edited
  afterwards, exactly as it would under Coolify.

**Not ours to decide:** a source template marked `# ignore: true` is one
Coolify does not list either.

## Image pinning

Nearly half of Coolify's images are `:latest`, and a mutable tag is re-pulled
on every deploy — so a bundled template carrying one would let a routine
redeploy cross a major version with nobody involved. `-pin` resolves every
reference against its registry and rewrites it to `repo@sha256:…`.

The digest form is also the one the agent can act on: `EnsureImage` skips the
pull when a digest reference is already local, precisely because those are
provably the right bits, while a tag always has to be re-fetched.

The human-readable version does not disappear — it moves to the template's
`version` field, taken from the routed application. When the source tag already
names a release (`1.2.3`) that is the version; when it does not, the importer
asks Docker Hub which of the repository's other tags point at the same digest
and takes the most specific. For registries without that listing API the
version is simply absent, and the catalog card shows the category alone.

Resolution also answers the other question a compose file often leaves open:
which port the application serves on. Docker reads it from the image's
`EXPOSE`, so the importer does too, and refuses when the image exposes nothing
unambiguous rather than guessing.

## Refreshing the catalog

Upstream templates move. To refresh:

1. `git -C ../coolify pull` (read-only reference, CLAUDE.md rule 1).
2. Delete the image cache — re-resolving is the point.
3. Re-run with `-pin -out templates/catalog`.
4. `go test ./templates/...` and read the diff. A changed digest is a version
   bump and should be treated as one; a template that stopped converting is a
   signal that upstream started using something the schema cannot express.
