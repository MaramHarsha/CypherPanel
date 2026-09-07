# Feature spec: The control plane backs itself up

> A nightly encrypted archive of everything the plane knows — projects,
> desired state, sealed secrets, users, audit log — pushed to an existing
> **Backup Target**, kept to a retention count, and restored by a `cypherd`
> subcommand on a host that has never seen this panel before. Agents re-adopt
> on their next heartbeat, desired state reconverges, and **nothing
> redeploys**, because the applications never stopped running.
>
> This is the [feature matrix](../product/feature-matrix.md)'s *"Panel-level
> backup/restore"* row, and the thing
> [panel-updates.md](panel-updates.md) §13 named and deferred: *"Nightly
> encrypted plane snapshots to a Backup Target, and `cypherd restore
> --from s3://…`, are disaster recovery — a separate feature with its own
> spec."*
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md), which gains two terms in the implementing PR
> (§9.1). The decisions this rests on are already made:
> [ADR-002](../adrs/ADR-002-agent-dial-home-no-ssh.md) (an agent's identity is
> its certificate, not a credential we store),
> [ADR-004](../adrs/ADR-004-traefik-file-provider.md) (routing config lives on
> the serving node), [ADR-005](../adrs/ADR-005-desired-state-reconciliation.md)
> (converging twice equals converging once).

## 1. This is not the HA control plane, and the difference is the whole design

[vision.md](../vision.md) puts *"Multi-region / HA control plane (v1)"* on the
**Explicitly out of scope** list, and names the anti-persona directly:
*"enterprise platform teams needing multi-region HA control planes."* A feature
that violates a non-negotiable is wrong even if it works, so the first thing
this spec owes is an argument that it is not that feature.

It is not, on four counts.

- **There is still exactly one control plane.** No second node runs, no leader
  is elected, no state replicates continuously, nothing fails over. The
  archive is an object in a bucket; a bucket is not a node.
- **Recovery is human-initiated and cold.** Somebody notices, fetches an
  object, runs a command, and starts a plane. HA exists precisely to remove
  that person and that minute; this exists to make that minute survivable.
- **The recovery point is a day, not a transaction.** An HA plane loses
  nothing. This loses up to a schedule interval — 24 hours by default — and
  §11 states the number rather than softening it.
- **The workloads were never down.** HA is about keeping the plane serving.
  This feature exists *because the plane not serving is survivable*: the apps
  keep running and keep answering while it is gone (§2).

The distinction that matters is between **availability** and **durability**.
vision.md declines to buy availability for the control plane, and that
declination is right: one plane, many workers, and a $5 VPS that runs the plane
and two apps. It says nothing about durability, and durability is not
optional — a solo developer whose panel VM is destroyed by their provider is
persona P1, not the anti-persona, and today the honest answer to "my panel host
is gone" is *re-enroll every server by hand and rebuild every project from
memory.* That is not a lightweight tool; that is a tool that punishes you for
using it.

So: **no failover, no standby, no replication. One archive, one command.**

## 2. What is actually at risk, and what is not

The plane holds desired state, identity and history. It does not hold
workloads, images, volumes or routes. That split is the architecture (ADR-002,
ADR-004, ADR-005) and it is what makes this feature small.

**Losing the plane does not stop a single application.** Containers keep
running because the agent that started them is still running and the Docker
daemon does not care that a NATS connection dropped. Traffic keeps arriving
because ADR-004 puts each node's Traefik behind a **file provider** whose
dynamic configuration was written to that node's disk by its own agent — no
lookup, no plane. TLS keeps renewing because the ACME account is carried in
`DesiredState` to each node and the certificate work happens there. An agent
whose plane is unreachable reconciles against the last desired state it holds
and keeps reporting into a socket nobody is reading.

What is at risk is everything in Postgres: 50 tables holding projects,
environments, applications, revisions, compose files, databases, backup
schedules, servers, users, teams, tokens, sealed secrets, the control-plane CA,
and the audit log. Threat-model asset **A6** ("desired state and audit
history") is the bulk of it, **A2** (the CA) is the one that makes re-adoption
possible, and **A3**/**A4** are the reason §4 exists.

The design screen states this to the operator in one sentence, and the copy
ships verbatim:

> Everything the plane knows — projects, desired state, sealed secrets, users,
> audit log — in one encrypted archive. **Apps and their data are untouched**:
> they live on your servers and keep serving.

## 3. The archive

One object per run, named for the moment it was taken:

```
<path_prefix>/2026-08-10T033000Z.tar.age
```

Inside, after decryption, an ordinary tar:

```
manifest.json            panel version, schema version, created_at,
                         table list with row counts, load order
master.key               the panel's CYPHERD_MASTER_KEY (§4)
tables/applications.copy.gz
tables/audit_events.copy.gz
…                        one member per table, gzip per member
```

Members are compressed individually rather than the tar as a whole, which is
why the object is `.tar.age` and not `.tar.gz.age` — the design screen's name
is literally right, `age -d … | tar t` lists the table names, and a restore
streams one table at a time instead of holding the archive in memory.

### 3.1 Why not `pg_dump`

`pg_dump` is the obvious answer and [panel-updates.md](panel-updates.md) §5
chose it for the *upgrade* snapshot, having rejected a hand-written logical dump
because it *"means owning foreign-key ordering, sequences, extensions and
constraint deferral for a schema that grows every release."* That reasoning is
sound for a snapshot taken by a root helper, on the panel's own host, one
version before it is restored. It does not survive the move off-host, on three
independent grounds.

**It violates vision.md non-negotiable 1.** *"One binary + one database to
install."* Making disaster recovery depend on `postgresql-client` being present
**and major-version-matched to the server** adds a second install — and the
operator finds out it is missing on the day their panel is gone. A recovery
procedure with an undeclared prerequisite is not a recovery procedure.

**It cannot run where the plane runs.** The runtime image is
`alpine:3.20 + ca-certificates + tzdata` and nothing else (`Dockerfile`, stage
3); there is no `pg_dump` in it and no Docker socket to `docker exec` through.
The systemd unit runs with `DynamicUser=true`, `ProtectSystem=strict`,
`PrivateTmp=true` and `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`
(`install/cypherd.service`) — deliberate hardening that panel-updates §3 leans
on for a different argument and that this feature must not ask to relax.
panel-updates' answer was a **root one-shot unit**; a *nightly, unattended* job
is a much worse thing to route through a root helper than a once-a-quarter
upgrade, and it would still leave the container install with no path at all.

**The restore side is worse.** A `pg_dump` archive carries the schema as it
was. A snapshot restored four months and three releases later has to land in a
schema the running binary can migrate forward from. The binary already carries
every migration (`//go:embed migrations/*.sql`, applied on boot); rebuilding the
schema from the snapshot's own migration version and then migrating up uses the
mechanism that is exercised on every single boot, instead of a second one that
is exercised on the worst day of the year.

### 3.2 What the plane does instead

The plane speaks the Postgres wire protocol already, and `pgx` exposes the
exact primitive `pg_dump` uses for table data: `PgConn.CopyTo(ctx, w, sql)` and
`PgConn.CopyFrom(ctx, r, sql)` (`pgx/v5/pgconn`). So:

- **Schema is not exported at all.** It comes from the binary's embedded goose
  migrations, replayed to `manifest.schema_version`. `goose_db_version` is
  therefore rebuilt by goose and deliberately excluded from the table list —
  copying it would put goose's bookkeeping in two places.
- **Data is exported per table** with `COPY <t> TO STDOUT` in COPY **text**
  format — the same representation `pg_dump` writes for data in plain format,
  with fully specified escaping and no version- or architecture-dependence.
  Binary format was considered and rejected for exactly that portability
  weakness; a snapshot has to open on a machine that is not this one.
- **The table list comes from the live catalog**, not a hand-kept list. A
  release that adds a table gets it in the archive the day it exists, with no
  second place to forget.

Three of the four things panel-updates §5 was unwilling to own do not exist in
this schema, which is checkable rather than asserted:

| Concern | In `core/store/migrations/` |
|---|---|
| Sequences | none — no `SERIAL`, no `nextval`; every id is a prefixed TEXT minted in `pkg/ids` |
| Extensions | none — no `CREATE EXTENSION` |
| Deferred constraints | none — no `DEFERRABLE` |
| FK ordering | **not owned by hand**: a topological sort of `pg_constraint`, computed at restore time from the catalog the migrations just created |

That last row is the one that has to keep being true. Load order is derived,
so a new table or a new foreign key needs no edit here; a *cyclic* foreign key
would break the sort, and the export fails loudly when it detects one — at
export time, in CI, rather than during a recovery.

### 3.3 What is deliberately not in it

**The data directory.** `CYPHERD_DATA_DIR` holds JetStream's WORK stream and
the runtime-log spool, and ENGINEERING rule 15 makes both transient by rule:
*anything that must survive a restart is in Postgres before it is published.*
Snapshotting a message spool captures in-flight work in a state that no longer
matches the database restored beside it. This is the same exclusion, for the
same reason, that panel-updates §5 makes.

**Anything belonging to a server.** No images, no volumes, no container state,
no application data. Those live on the servers, which is the point (§2), and
volume backups are their own feature-matrix row.

## 4. The master key is the crux

A snapshot of this database is **worthless without `CYPHERD_MASTER_KEY` and
catastrophic with it.** Both halves are literally true and the design follows
from taking both seriously.

*Worthless*: `plane_ca.encrypted_key` — the CA that signs every agent
certificate, threat-model asset **A2** — is sealed with the master key, and
`identity.LoadOrCreateCA` fails closed on a wrong key ("decrypting CA key
(wrong master key?)") rather than quietly minting a new CA. A plane restored
without the key does not boot, let alone re-adopt anything. Every application
env var, deploy key, database root password, registry credential, DNS token and
S3 credential is sealed the same way.

*Catastrophic*: the archive plus the key is A2, A3 and A4 in one file.
threat-model §5.1 already lists the vector by name — control-plane compromise
via *"read access to its Postgres (e.g. via a TB1 API vulnerability, a
supply-chain compromise, or **a stolen backup**)."* An unencrypted plane
snapshot in a bucket is strictly worse than the database it came from: the
database sits behind the panel's authentication on a host with one purpose,
and the object sits behind an S3 key pair that gets copied into CI, laptops and
terraform state.

Three designs were considered.

**Rejected — leave the key out and tell the operator to keep it.** The panel
already does this. `install.sh` writes, into a file the operator opens once:
*"CYPHERD_MASTER_KEY decrypts every sealed secret… Back it up. If you lose it,
those secrets are unrecoverable."* Nobody does. The failure is silent, total,
and discovered on the one day it matters, after a year of green checkmarks. A
recovery story whose critical prerequisite is an unenforced comment is a
recovery story that does not exist.

**Rejected — seal the archive with the master key.** Correct for
panel-updates' local upgrade snapshot, which never leaves the host and is
restored by a helper that reads the same root-owned env file. Off-host it is
circular: you need the key to open the archive that contains the key.

**Rejected — a passphrase in the plane's configuration.** For the nightly run
to be unattended the passphrase has to live on the plane host, beside the
master key. An attacker with one now has both, the archive gains no protection
the database does not already have, and the operator has gained a second secret
to lose.

**Chosen — asymmetric.** The plane stores an age **recipient** (a public key)
and can only ever *write*. The private half — the **Recovery Key** — is
generated once, shown once, and never stored by the panel: not in Postgres, not
in the data directory, not in a log line, not in an audit detail. There is no
API that returns it and no configuration variable that holds it.

Four consequences, and they are the reason this shape wins:

1. **The nightly run needs no new secret.** Encrypting to a public key requires
   nothing private.
2. **The archive may safely carry the master key**, because it is now protected
   by something the plane does not hold. That collapses the operator's
   obligation from *two* secrets — one of which they have already forgotten —
   to **one**.
3. **A leaked bucket yields nothing.** This is the realistic exposure: S3 keys
   spread, buckets get misconfigured, storage providers have incidents. Against
   an attacker who already owns the plane it buys nothing — they can read the
   live database — and this spec does not claim otherwise.
4. **The operator is asked for the one secret at the one moment they are
   paying attention**: while turning the feature on, not in a comment.

### 4.1 age, and why a dependency

The container format is **age** (`filippo.io/age`), X25519 recipient. The value
of a standard format is the escape hatch: `age -d -i recovery.key
snapshot.tar.age | tar t` works with **no `cypherd` at all**, which is the
property that matters when the panel is the thing that died. Hand-rolling the
container instead would mean a second cryptographic file format in this
repository, written to be read once, under pressure, years later.

The cost is named rather than hidden: one small pure-Go module plus
`filippo.io/edwards25519`, against ADR-001's single-binary discipline. It is
the smallest new dependency in the tree and it replaces code we would otherwise
have to write and defend.

### 4.2 The handling rules, which are the feature

- **Generated in memory** when disaster recovery is armed with
  `recipient_mode: generate`. The identity is in the `PUT` response **exactly
  once** — the pattern managed-databases §2 uses for a root password and
  invitations uses for an accept link.
- **Only the recipient is persisted.** `plane_dr_config.recipient` is a public
  value; there is no column for the other half.
- **Arming does not complete until the operator proves possession.**
  `POST /api/v1/panel/dr/verify-key` takes the pasted key, derives the
  recipient from it, compares, and stamps `recipient_verified_at`. Until then
  the schedule does not run and the settings card says so. This is the
  enforcement the env-file comment never had, and it is the difference between
  backups and the *appearance* of backups.
- **Possession is re-provable at any time**, offline, with
  `cypherd dr verify-key` — derive and compare, decrypt nothing, contact
  nothing.
- **Bring your own recipient.** `recipient_mode: provided` accepts an age
  recipient the operator already manages (a hardware-backed identity, a team
  recipient). The panel then never sees a private key at all; the verify step
  is skipped and the reason recorded, because there is nothing for the panel to
  verify against.
- **Rotation re-keys nothing.** A new recipient applies to future snapshots
  only; archives written to the old one stay readable by the old key alone. The
  sidecar (§5.2) records which recipient each object was written to, so the
  answer to "which key opens this" is in the bucket rather than in somebody's
  memory.
- **Session-only.** Arming, rotating and verifying are registered through
  `sessionOnly` and require the panel `owner` role, alongside `PUT
  /api/v1/panel/tls`. An API token inherits its owner's authority, and
  deploy-protection established the rule this follows: a credential that runs
  unattended must not be able to arm — or silently re-point — the control that
  protects everything.

## 5. Where it goes

### 5.1 An existing Backup Target, deliberately

The destination is a **Backup Target** (`backup_targets`), the same rows
managed databases already back up to — not a second S3 configuration. One place
S3 credentials are sealed, one test path, one thing to rotate. The operator who
has already set up off-site storage for their databases has already made the
decision this feature needs, and an operator who has not is offered that screen
from the empty state (§10).

The reference is `ON DELETE RESTRICT`, matching `database_backups.target_id`: a
target the plane snapshots to cannot be deleted from under the schedule, and the
409 names what is using it.

The credential wants `PutObject`, `DeleteObject` (retention) and `ListObjectsV2`
(index rebuild) on the prefix, and nothing else. The panel cannot enforce that —
it is a bucket policy — but the settings copy recommends it, because a
write-and-prune credential that cannot `GetObject` is a materially smaller
loss when it leaks.

**These requests deliberately do not go through `core/egress`.** That guard
exists for URLs a *user* supplies to be probed — an unsaved notifier, a registry
credential test — where the attacker chooses the address. A Backup Target is
operator-configured infrastructure, and a MinIO on the same LAN is the ordinary
self-hosted case. Refusing private addresses here would break the most common
setup in the name of a threat this path does not have. Recorded because the
inconsistency is otherwise exactly the kind of thing a reviewer should flag.

### 5.2 The sidecar, and where truth lives

panel-updates §5 found a problem worth restating: **the record of which
snapshots exist lives in the database the snapshots are of.** Restoring one
rewinds that record, erasing the row for every snapshot taken since — possibly
including the one the operator wants next. Its answer was to make a local
directory the source of truth and the table an index.

Here the local directory is the thing that burned down. So **the bucket is the
source of truth**, and each object is written with a small plaintext sidecar
beside it:

```
2026-08-10T033000Z.tar.age
2026-08-10T033000Z.tar.age.json    { id, panel_version, schema_version,
                                     created_at, size_bytes, sha256,
                                     recipient, row_count }
```

The sidecar names nothing secret — a version, a size, a digest, and a public
key — and it is what lets `cypherd restore --list` enumerate a bucket with no
database and no panel anywhere in sight. `plane_snapshots` is an index rebuilt
from a bucket listing on the first sweep after boot and on demand.

### 5.3 Schedule, retention, failure

The schedule is a cron expression (default `30 3 * * *`, matching the design's
`nightly 03:30`) fired by the sweeper pattern that already exists — the shape of
`Scheduler.SweepDueBackups`, `robfig/cron`, on `CYPHERD_SWEEP_INTERVAL`, with
`last_run_at` advanced when a run *starts* so a schedule fires at most once per
due window. No catch-up storms; a run missed while the plane was down fires on
the next sweep, not as a backlog. That contract is managed-databases §7's,
restated because it is being reused rather than reinvented.

Retention is a count (default 14, the design's `keep 14`), applied by the plane
itself after a **successful** upload, oldest first. There is no work item and no
agent: the object is the plane's own database, and shipping it to a worker so
the worker could delete it would put A2+A3+A4 on a worker's disk to save the
plane an HTTP request — this feature's threat model exactly inverted.

Retention deletes only objects whose sidecar the plane can read and attribute.
An object it cannot identify is left alone, on the asymmetry
[disk-management.md](disk-management.md) §2 records: keeping one too long costs
storage, and deleting one too early costs the recovery.

Failure surfaces as two panel-level inbox kinds fired **on the transition**:

```
panel.snapshot_failed      (error)
panel.snapshot_recovered   (info)
```

Panel-level rather than subscribable for the structural reason
disk-management §5 established and panel-updates §2 restated: a **Notifier** is
scoped to one Project and the control plane belongs to none, so there is no
channel to resolve. On the transition rather than per run, because a job that
mails every night gets filtered, taking the one real alert with it.

And because *a backup system that fails quietly is worse than no backup system*,
the settings card's headline is the last result — `✓ 03:30 · 18 MB` — not a
configuration summary.

## 6. Restore

The design screen is the specification for the operator-facing half:

```
# losing the panel VM is a 5-minute incident:
$ cypherd restore --from s3://b2-backups/plane-state/2026-08-10.tar.age
agents re-adopt on next heartbeat — desired state reconverges, nothing redeploys
```

It is a subcommand of the same binary — one artifact to build, sign and ship,
the reasoning panel-updates §3 applies to `cypherd upgrade` — and it runs with
the plane **stopped**. There is no restore button in the panel, and §13 records
why there never will be.

`--from` accepts an `s3://` URL (with `--endpoint`, `--region`, and credentials
from flags or environment) **or a local path**. The local path is not a
convenience: the S3 credentials for the target are *inside* the archive, so an
operator who no longer has them separately has to fetch the object with their
provider's own tooling first, and the command must accept the result.

The sequence, and what each step refuses:

1. **Fetch and decrypt** with the Recovery Key (`--identity`, a file — never an
   argv value, which is world-readable through `ps`, the same rule
   [pack-builds.md](pack-builds.md) applies to Nixpacks env). When a sidecar is
   present the SHA-256 is checked; a mismatch is a refusal, not a warning.
2. **Read `manifest.json`.** Panel version, schema version, table list, row
   counts, created_at — printed before anything is written, so the operator can
   see they grabbed the right night.
3. **Refuse a non-empty target database** unless `--force`. Restoring over a
   live panel is how a fleet ends up with two half-planes.
4. **Migrate to the snapshot's schema version.** A snapshot from a *newer*
   panel than the binary is refused, naming the version to install — that
   restore cannot work and the honest answer is one line long.
5. **Load every table** with `COPY … FROM STDIN`, in the computed order, inside
   **one transaction**. All or nothing; a failed restore leaves an empty
   database rather than half a panel.
6. **Migrate the rest of the way up** to the binary's own version, through the
   ordinary goose path.
7. **Write the restore into `audit_events`** in the same transaction — actor
   `system`, action `panel.restored`, detail naming the snapshot id, its
   `created_at` and the object key. The evidence that the audit log was rewound
   lands inside the rewound audit log, which is where anyone will look for it.
8. **Print the master key** and the exact `CYPHERD_MASTER_KEY=` line to add,
   or write it 0600 with `--write-env <path>`. This is a secret on a terminal
   and it is the right call: the operator is already holding the Recovery Key,
   which is strictly more powerful. It goes to stdout and **never through
   `slog`** — ENGINEERING rule 20 is about logs, errors and API responses, and
   this is none of the three, but the distinction has to be deliberate rather
   than accidental.
9. **Print the summary**, including every server it re-admitted, by name and
   enrolment date (§8).

### 6.1 Testing the backup, because untested backups are not backups

The feature matrix says it on the restore row — *"backups without tested restore
fail P2 (Alex)"* — so three affordances exist and none of them is a promise
that it will work:

- `cypherd restore --verify --from …` runs steps 1–2 and parses every table
  member, and **writes nothing.** The drill.
- `cypherd restore --list --from s3://…` enumerates a bucket from its sidecars,
  with no database and no panel.
- **The plane proves the round trip once per target.** After the *first*
  successful upload to a newly configured target, the plane re-downloads the
  object and re-hashes it. Once, while the operator is watching — not nightly,
  which would buy egress every night for a fact that does not change. It cannot
  verify decryption, because it does not hold the key, and the UI says exactly
  that rather than implying a green tick means restorable.

## 7. Why the agents come back

The design screen's claim — *"agents re-adopt on next heartbeat — desired state
reconverges, nothing redeploys"* — is not something this feature implements. It
is something the existing architecture already guarantees, and the value of
writing it down is knowing which parts are load-bearing.

**Re-admission.** `core/bus`'s authorization callback reads the client
certificate's Common Name, asks `AgentEnrolled(cn)`, and grants that server's
subject scope. There is no per-certificate fingerprint stored and no session
state on the plane. So an agent is admitted if and only if (a) its certificate
chains to the plane CA and (b) a `servers` row with that id exists and is
enrolled. The restore returns both: the sealed CA key (openable because the
master key rode along, §4) and the `servers` rows. Nothing to re-enroll, no join
token, no shell on any host.

That model also makes the restore immune to certificate churn. Agent
certificates renew themselves at two thirds of their 90-day life over the mTLS
channel, with a fresh key the plane keeps no copy of
([agent-identity-and-tls.md](agent-identity-and-tls.md) §3) — so a snapshot
taken before a renewal is not stale in any way that matters.

**Reconvergence.** The agent reconnects, requests desired state on
`state.<server>.sync`, receives `DesiredStateFor(server)`, and reconciles — and
finds that everything already matches, because the containers never stopped.
*Converging twice equals converging once* is ENGINEERING rule 13 and every
reconciler ships a test for it; that rule is the entire reason a restore is a
no-op at the workload layer. Nothing rebuilds, nothing restarts, no route
flaps.

### 7.1 The one thing that does not come back by itself

**Agents dial a name they hold locally.** `agent/identity` persists
`PlaneAddr` and `NATSURL` in the agent's state directory. If the restored plane
answers on a different hostname, agents keep dialing the old one and **do not
re-adopt** — the panel comes back with a fleet that looks permanently offline.

So the recovery procedure is: **restore behind the same DNS name.** Point the
old record at the new host and the fleet returns on its own; stand the panel up
at a new name and every server needs a visit. The design screen's "5-minute
incident" is honest under the first condition and not under the second, and the
settings card and the restore command's own output both say so rather than
letting an operator discover it at 03:00.

Fixing this properly means changing the plane's address on an agent that can no
longer reach the plane — a chicken-and-egg that needs its own feature and
probably its own ADR (a signed, agent-side fallback address list). It is named
here as a gap, not papered over.

## 8. What a restore rewinds, stated plainly

A restore is a rewind of the panel's memory. Everything below is a real
consequence, and the design decision in each case is which of *silently do it*,
*silently prevent it*, or *do it and say so* is honest.

**Revocations come back.** Removing a `servers` row is how an agent identity is
revoked. A snapshot taken before a revocation restores that row, and the revoked
agent — if it still holds its certificate and still runs — is re-admitted. There
is no mechanism that fixes this without inventing a revocation ledger outside
the database, which is a different feature with its own ADR. The answer is
**disclosure**: the restore summary lists every server it re-admitted, and the
post-restore checklist in the docs says to re-check them. Naming it beats a
control that pretends to solve it.

**Sessions are purged.** Deliberately, on load: every `sessions` row is dropped
rather than restored. Everyone signs in again. The cost is one login at a moment
when a human is already at a keyboard; the benefit is that a session signed out
during the window does not come back to life, and a removed user's session
cannot return with their row.

**API tokens are kept.** The asymmetry is deliberate and stated: a human
re-authenticates in ten seconds, and a fleet of CI configurations does not.
Purging them would turn a panel outage into a deployment outage that outlives
it. A token revoked during the window does return; the summary counts tokens
restored so the operator can re-revoke.

**In-flight work is not resumable.** The WORK stream is not in the archive
(§3.3), so a deployment that was `building` or `rolling_out` at snapshot time
restores with no work item behind it. Those are swept to `error` on the first
boot after a restore, with a detail naming the restore — because a status that
shows as in-progress forever with nothing behind it is precisely the mystery
spinner vision.md forbids. The remedy the error offers is "deploy again", and
the application itself is untouched meanwhile.

**Runtime and build logs are gone.** They live in the data directory under
bounded retention ([bounded-log-retention.md](bounded-log-retention.md)); they
were expiring anyway.

**Previews and schedules resume rather than catch up.** A preview environment
whose TTL expired during the outage is reclaimed by the first sweep — that looks
like data loss and is not. Scheduled tasks and database backup schedules fire on
their next due window with no backlog, per the sweeper contract §5.3 reuses.

**Everything created after the snapshot is gone.** That is the recovery point,
and §11 gives it a number.

## 9. Data model, API, configuration

### 9.1 Vocabulary

Two glossary entries land in the same PR, as ui-principles §8 requires (*copy
that needs a term the glossary lacks means the glossary gets a PR first*):

- **Plane Snapshot** — one encrypted archive of the entire control-plane
  database, written to a Backup Target and readable only with the panel's
  Recovery Key. Distinct from a **Backup Record**, which is one dump of one
  Managed Database, and from the local upgrade snapshot panel-updates §5 keeps
  on the panel's own disk.
- **Recovery Key** — the private half of the key pair a Plane Snapshot is
  encrypted to. Generated once, displayed once, never stored by the panel. It is
  the only thing that can open a Plane Snapshot, and it is exactly as powerful
  as the panel's master key, because the master key is inside.

"Plane" as the short form of Control Plane is already the schema's own
vocabulary (`plane_ca`) and this repository's prose throughout; the glossary
entry makes it explicit rather than introducing it.

### 9.2 Migration `0040_plane_disaster_recovery.sql`

Additive; `0039_server_disk.sql` is the current highest. Two tables, no
existing table altered.

```
plane_dr_config          -- singleton, on panel_mail's and panel_tls's shape
  id INTEGER PK DEFAULT 1, CHECK (id = 1)
  target_id       TEXT NOT NULL → backup_targets(id) ON DELETE RESTRICT
  path_prefix     TEXT NOT NULL DEFAULT 'plane-state'
  schedule        TEXT NOT NULL DEFAULT '30 3 * * *'
  retention_count INTEGER NOT NULL DEFAULT 14
  recipient       TEXT NOT NULL          -- age recipient (public)
  recipient_mode  TEXT NOT NULL          -- 'generated' | 'provided'
  recipient_verified_at TIMESTAMPTZ      -- NULL ⇒ armed but not proven (§4.2)
  last_run_at     TIMESTAMPTZ
  last_status     TEXT NOT NULL DEFAULT ''    -- succeeded | failed | running
  last_detail     TEXT NOT NULL DEFAULT ''
  created_at, updated_at

plane_snapshots          -- an INDEX; the bucket is the truth (§5.2)
  id TEXT PK (snap_…)
  object_key      TEXT NOT NULL UNIQUE
  panel_version   TEXT NOT NULL
  schema_version  BIGINT NOT NULL
  size_bytes      BIGINT NOT NULL DEFAULT 0
  sha256          TEXT NOT NULL DEFAULT ''
  row_count       BIGINT NOT NULL DEFAULT 0
  recipient       TEXT NOT NULL DEFAULT ''
  status          TEXT NOT NULL DEFAULT 'running'
  detail          TEXT NOT NULL DEFAULT ''
  started_at, finished_at, pruned_at
```

The singleton row exists only while disaster recovery is armed; disarming
deletes it, so "is this panel backing itself up?" is one question with one
answer, exactly as `panel_tls` handles the same shape.

`recipient_verified_at NULL` is load-bearing rather than cosmetic: the sweeper
skips a config that has not been proven (§4.2), so an operator who closed the
tab mid-setup gets a card that says *"waiting for your recovery key"* instead of
a year of archives nobody can open.

### 9.3 API — seven operations under `/api/v1/panel/dr`

```
GET    /api/v1/panel/dr                    → config + last result
PUT    /api/v1/panel/dr                    → arm/update  (recovery_key once)
DELETE /api/v1/panel/dr                    → disarm; leaves every object alone
POST   /api/v1/panel/dr/verify-key         → prove possession, arm the schedule
POST   /api/v1/panel/dr/run                → 202, snapshot now
GET    /api/v1/panel/dr/snapshots          → the index
POST   /api/v1/panel/dr/snapshots/refresh  → rebuild the index from the bucket
```

198 operations today, 205 after. Every one is panel `owner`, alongside `PUT
/api/v1/panel/tls` and for the same reason that handler gives — this decides
where a copy of every secret in the panel is written. The mutating four are
additionally `sessionOnly` (§4.2). No route needs adding to
`panelScopePrefixes`: the `/api/v1/panel/` prefix already covers them, so a
project-scoped token cannot reach any of it.

`GET` never returns the Recovery Key, because the panel does not have it. It
returns the recipient, which is public.

### 9.4 Audit and inbox

Additive entries in the closed audit vocabulary (`core/audit/actions.go`),
all `ResourcePanel`:

```
panel.dr_updated · panel.dr_disabled · panel.recovery_key_generated
panel.recovery_key_verified · panel.snapshot_created · panel.snapshot_pruned
panel.restored
```

Details carry the target name, prefix, schedule, retention, object key and
digest — never the Recovery Key, never the master key, never an S3 credential.
`panel.snapshot_created` is one row per night, which is what an audit log is
for.

Two inbox kinds join `panelInboxKinds` in `core/domain/inbox.go`:
`panel.snapshot_failed`, `panel.snapshot_recovered` (§5.3).

### 9.5 Proto: no change

Nothing crosses the agent boundary. `buf breaking` has nothing to compare
because there is nothing to compare. The reflex is to reach for a work item and
the reason to refuse is §5.3's: the archive is the plane's own database, and
routing it through an agent would put the CA and every sealed secret on a
worker's disk.

### 9.6 One S3 client, moved

`agent/driver/docker/s3.go` is a 190-line SigV4 implementation of PUT, GET and
DELETE with no SDK, written for the vision's footprint budget. The plane needs
those three plus LIST. It moves to `pkg/s3` and both sides import it — one
implementation of request signing, for the reason `core/egress`'s package doc
already gives about security-relevant code: *"two implementations… is how one of
them quietly stops matching the other."* LIST is the only new verb.

### 9.7 Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_SNAPSHOT_TIMEOUT` | `30m` | Ceiling on one export-and-upload run. Exceeded ⇒ the run fails and the inbox says so; it never blocks a second attempt the next night. |

And deliberately **no variable for the Recovery Key**, in either direction.
There is nowhere on the plane it could live that would not undo §4.

### 9.8 The relationship to `panel-updates.md` §5

panel-updates §13 asked that this feature *"adopt this snapshot format rather
than invent a second one."* The requirement is right — one format — and this
spec resolves it the other way round, which the implementing PR records as an
amendment to panel-updates §5 rather than a silent divergence (ENGINEERING rule
33).

The reasons are §3.1's: the `pg_dump` ladder cannot run unattended from the
plane, cannot run at all on the container install, and produces an archive whose
schema is fixed at dump time. None of those matter for a local snapshot taken
one version before it is restored, and all of them are fatal for an off-site
snapshot restored months later on a different host.

What the upgrade case keeps is its *timing*, which is the part that was actually
load-bearing there: the snapshot is taken after the read-only lock is held, so
nothing written after it is silently lost by a restore claiming to rewind to the
moment of upgrade. The helper still triggers it; it simply calls the same export
in the same binary — which panel-updates §3 already contemplates ("the unit runs
the same binary under a different entry point"). `plane_snapshots` is shaped to
be the table panel-updates §5 describes, so the two are one table with a `kind`
column when that feature lands, and not two.

## 10. The screen

One card, Settings → Disaster recovery
(`web/src/routes/_app/settings/disaster-recovery.tsx`), owner-only, with the
design's copy shipping verbatim:

- **Plane snapshot** · `nightly 03:30 → b2-backups / plane-state · encrypted ·
  keep 14`, with the last result as the right-hand status: `✓ 03:30 · 18 MB`.
- The body paragraph from §2, with *"Apps and their data are untouched"* in
  emphasis — it is the sentence that stops a panicking operator from doing
  something destructive.
- The restore command in an ink-on-ink block in **both** themes
  (ui-principles §9: log and terminal panes are ink in both), copyable.
- The footer line: *"The recovery story rides the architecture that already
  exists: state is declarative, agents dial home, containers don't care where
  the plane went."*

The four-state contract (ui-principles §1):

- **Loading** — skeleton at the card's final height.
- **Empty** — *"Nothing is backing up the panel yet."* When no Backup Target
  exists, the primary action is *"Add a backup target"* and links there;
  chained empty states, ui-principles §11, rather than a form that fails on
  submit.
- **Error** — the last failure's reason in glossary terms with its likely
  remedy (*"The bucket refused the upload — the key may not have PutObject on
  `plane-state/`"*), raw detail behind an expander.
- **Content** — as above, plus the snapshot list.

Arming is one drawer (modal depth 1, ui-principles §4): target and prefix,
schedule and retention, then the Recovery Key — revealed once with a copy button
(§6: every generated value has one), with the plain-language line beneath it
that ui-principles §11 requires: *"Recovery key — the only thing that can open
these snapshots. We show it once and never store it."* **The form does not
submit until the key is pasted back.**

Two pieces of honesty the screen owes:

- A card whose `recipient_verified_at` is NULL reads *"Waiting for your recovery
  key — snapshots are paused"*, in amber, not green.
- A run the plane could not verify shows `unknown`, hollow, never a green tick
  (ui-principles §5, §10: never fake certainty). The plane holds no key; it
  cannot know an archive opens.

Turning it off states its blast radius without demanding a typed name, because
it destroys nothing: *"Stops nightly snapshots. The 14 archives already in
b2-backups are left alone."*

## 11. Cost and risk, honestly

**Recovery point: up to 24 hours.** Everything created after the last snapshot
is gone — a project made this morning, a deploy from an hour ago, today's audit
trail. Tightening the schedule tightens the window at the cost of more objects;
there is no continuous option here and §13 says why.

**Recovery time: minutes, conditionally.** Fetch, decrypt, migrate, load, boot.
A panel database is dominated by `audit_events`, `inbox_items`,
`webhook_deliveries` and `deployments`, each already retention-bounded, so the
archive grows with fleet activity and not without limit. The "5-minute incident"
holds when the restored plane answers at the same DNS name (§7.1) and not
otherwise.

**Storage: retention × archive size**, on the operator's own bucket.

**Risk — the operator loses the Recovery Key.** Every archive becomes
unopenable, permanently. This is a deliberate, disclosed trade against the
alternative in §4, and the mitigations are the verify-at-arming step and
`cypherd dr verify-key` on demand. There is nothing else, because there is
nothing else honest: a copy of the key that the panel could produce on request
is a key the panel holds.

**Risk — a compromised plane can delete every archive.** It holds the
credential that prunes them. The answer is object-lock or versioning on the
bucket, which is the operator's policy and not something the panel can promise
to have applied (§13).

**Risk — the export must stay correct as the schema grows.** Mitigated by
construction: tables are enumerated from the live catalog and load order is
computed from `pg_constraint`, so a new table needs no edit here. The acceptance
test is a real-Postgres round trip (ENGINEERING rule 29) that seeds every table,
snapshots, restores into a fresh database, and compares row counts and
per-table checksums — dynamic enumeration means a table added next release is
exercised the day it exists.

**Risk — it is untested until it is used.** The oldest failure in this whole
category. §6.1's three affordances reduce it; none of them eliminates it, and
this spec does not claim they do.

## 12. Acceptance (testable)

1. Arm disaster recovery against a live MinIO target with `generate`; the
   response carries a Recovery Key exactly once, a second `GET` never returns
   it, and the schedule does not run until `verify-key` succeeds.
2. `verify-key` with a key from a different pair is refused; with the right one
   it stamps `recipient_verified_at` and the sweeper begins.
3. A scheduled run writes an object and its sidecar; the object decrypts with
   `age -d -i` and `tar t` lists one member per table plus `manifest.json` and
   `master.key`.
4. Retention at `keep 3`: a fourth successful run prunes the oldest object and
   its sidecar and marks the row `pruned_at`; a fourth *failed* run prunes
   nothing.
5. Wipe the database and the data directory, then `cypherd restore --from` the
   object on a host with a different id: the plane boots, and a previously
   enrolled agent — untouched, still running its containers — reconnects,
   syncs, and reconciles with **zero container recreations** (asserted on the
   agent's own reconcile counters, not inferred from status).
6. The same restore against a snapshot taken from a *newer* panel build is
   refused, naming the version to install; against a non-empty database it is
   refused without `--force`.
7. A restore whose object was truncated in transit fails the digest check and
   writes nothing.
8. The Recovery Key, the master key and the target's S3 secret appear in no log
   line, no audit detail and no API response — asserted over a full arm →
   snapshot → prune → restore cycle.
9. `restore --verify` and `restore --list` complete against a bucket with no
   database configured at all.
10. Sessions are absent after a restore and API tokens are present, and the
    summary names every re-admitted server.

## 13. Deliberately out of scope

- **Failover, a standby plane, or continuous replication.** vision.md's
  explicit non-scope (§1). One plane, many workers; there is nothing to fail
  over to and no election to run.
- **Point-in-time recovery.** WAL archiving is an operation on the operator's
  own Postgres, which may be RDS or a managed service the panel has no
  administrative access to. managed-databases §11 excludes PITR for its
  databases on the same reasoning, and taking it on here would mean the panel
  managing the lifecycle of a database it does not own.
- **A restore button in the panel.** The thing being restored is the thing that
  would run the restore. panel-updates §3 makes this argument about a binary
  swap; here it is absolute rather than merely strong.
- **Automatic restore, on any trigger.** ADR-010's rule for the panel's own
  lifecycle: the plane never performs the irreversible operation on itself
  unattended. A restore that fires on a heuristic is a way to lose a day of
  work to a network partition.
- **Backing up application data, volumes or images.** They live on the servers
  (§2), volume backups are their own feature-matrix row, and images are
  reproducible from revisions by construction (ADR-008).
- **Managing bucket immutability.** Object-lock, versioning and MFA-delete are
  the right controls against §11's delete risk, and they are bucket policy. The
  panel will recommend them in copy and will not claim to have applied them.
- **Multiple recipients, key escrow, or a KMS.** age makes a recipient *list*
  nearly free, and it is the obvious answer to "what if I lose the key" — which
  is exactly why it needs its own decision: every additional recipient is
  another key that opens every historical archive, and the panel cannot tell
  the operator where those keys are. `recipient_mode: provided` (§4.2) is the
  escape hatch for anyone who already runs a key ceremony. External KMS for the
  master key is threat-model §5.1's own post-v1 note and is a larger change than
  this one.
- **Encrypting the archive with the master key.** §4, rejected with reasons.
- **Snapshotting the data directory.** §3.3.
- **A second S3 configuration.** Backup Targets exist; §5.1.
- **Cloning a panel from a snapshot.** A restore re-admits agent identities, so
  two planes restored from one archive both believe they own the same fleet,
  with the same CA and the same server ids. That is a split brain nobody can
  debug from the panel. The restore cannot detect it and does not try; it is
  named here so that "can I use this to migrate to a new panel?" has a written
  answer, which is: only by moving, never by copying.
- **Per-table or selective restore** ("restore just the projects"). A partial
  load into a live schema breaks referential integrity in ways that surface
  weeks later, and the transaction boundary in §6 is what makes the failure
  mode "nothing happened" instead of "something happened".
