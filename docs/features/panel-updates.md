# Feature spec: Panel update alerts, guided upgrade and version history

> The other half of [ADR-010](../adrs/ADR-010-agent-auto-update.md). Agents
> converge on a desired version by themselves; **the panel never does.** It
> tells its owner a release exists, and — when the owner asks — runs one
> guided upgrade with a pre-flight, a fallback snapshot whose retention the
> owner picks, a health gate, and an automatic rollback when the gate fails.
>
> This is the [feature matrix](../product/feature-matrix.md)'s
> *"Auto-update of the platform (safe-by-design)"* row and
> [community-pain-points.md](../../research/community-pain-points.md) §1: the
> **#1 trust wound** in the reference platforms — an update button that ran the
> updater twice and destroyed `.env` including the encryption keys
> ([coolify#3687](https://github.com/coollabsio/coolify/discussions/3687)), a
> proxy broken by an update (coolify#7193), a version-to-version update failure
> (coolify#7599). Every design decision below is downstream of not repeating
> those three.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. What exists, and what this adds

Three pieces already ship and are not re-litigated here.

`GET /api/v1/panel/version` reports the running build and, when the check is on
and has found one, `latest` — version, `kind` (`patch|minor|major`),
`notes_url`, `published_at` ([control-plane-hardening.md](control-plane-hardening.md)
§3). The `core/updates` checker is hardened as threat-model §5.13 requires:
opt-out, 10 s timeout, 256 KiB body cap, three redirects, none to a private
address, failure means silence. And one `panel.update_available` inbox item per
version reaches every owner who has not muted the kind.

[release-signing.md](../dev/release-signing.md) fixes how a release is proved:
`SHA256SUMS` plus a detached `SHA256SUMS.sig`, ed25519, signed **offline** by a
human against a local rebuild of the tag, never by CI. `cmd/release-sign
-verify` is the verifier that already exists.

What is missing is everything between "there is a newer version" and "I am
running it". Today that gap is a shell, a `curl`, a `systemctl restart`, and
the operator's own nerve. This spec closes it with four things: the alert
(§2), the machinery that performs a swap the plane itself is not allowed to
perform (§3), the guided upgrade — pre-flight, snapshot, health gate,
auto-rollback (§4–§6) — and version history, where rollback is honest about
what it can and cannot promise (§7–§8).

It is the panel's own lifecycle only. Agent versions are
[agent-updates.md](agent-updates.md), and the two are ordered: that spec's plane
**refuses a desired agent version newer than its own build**, so the sequence is
always *upgrade the panel, then promote the fleet*. The changelog says as much
in the copy — *"Agents update themselves; the panel waits for you."*

## 2. The alert

One banner, owners only, at the top of the app shell — the slot `sse-banner`
already occupies for the reconnect notice (ui-principles §10):

> ● **CypherPanel v1.1.0 is out** — you're on v1.0.3   `MINOR · NEW FEATURES`
> `Review & update`   `Later`

The badge is `latest.kind`, which the API has carried since
control-plane-hardening §3, and it exists to answer the only question an
operator actually has, which is *how carefully do I have to read this*:

| | |
|---|---|
| `PATCH` | 1.0.3 → 1.0.4 · fixes only, update anytime |
| `MINOR` | 1.0.x → 1.1.0 · new features, read the notes |
| `MAJOR` | 1.x → 2.0 · breaking changes, plan a window |

Three surfaces, and no fourth: **① this banner (owners only) ② the inbox item
③ a dot on the help menu → changelog.** Never a modal takeover, never an
auto-install (ADR-010: operator-driven). A modal would be the one interaction
that is wrong here — the panel interrupting an operator mid-incident to ask
about a feature release is how a product teaches people to dismiss it without
reading.

`Later` is per user **and per version**: it stores `latest.version` on the user
row, so dismissing v1.1.0 does not hide v1.2.0. It is deliberately server-side
rather than `localStorage`, because a dismissal that does not follow the
operator to their other browser is a banner they learn to ignore.

**One deviation from the design canvas, recorded rather than quietly dropped.**
The canvas lists surface ② as "inbox + email digest". The inbox item ships
today; the email does not, and cannot yet, for the structural reason
[disk-management.md](disk-management.md) §5 already recorded about
`server.disk_low`: a panel-level kind belongs to no Project, and a **Notifier**
is scoped to one. There is no channel to deliver it to, and delivering it to
every project's channels would be worse than not delivering it. Panel-level
notifiers are the missing primitive; naming that gap is more useful than
inventing a second, private mail path.

## 3. Who performs the swap, and why it is not the plane

The obvious implementation — the plane downloads its own successor, writes it
over `/usr/local/bin/cypherd`, and re-execs — is refused on three separate
grounds, any one of which is sufficient.

**It cannot work as installed.** `install/cypherd.service` runs the plane with
`DynamicUser=true` and `ProtectSystem=strict`. `/usr` is read-only to that
process and it is not root; it cannot write its own binary. Relaxing that with
a `ReadWritePaths=/usr/local/bin` is not a small edit — it converts any
remote-code-execution bug in the API surface (threat-model §5.8) into
persistence on the control-plane host. That is precisely the blast radius
ADR-010 §3 refuses to hand a compromised plane over the fleet, and it would be
strange to refuse it there and grant it here.

**A process cannot health-gate its own replacement.** The thing that decides
whether the new build is alive is the thing being replaced. If the new binary
crashes on boot, nothing is left running to notice.

**ADR-010 says the plane never updates itself**, and the sentence should stay
literally true rather than become a slogan the implementation quietly steps
around.

So the swap is performed by a **separate, root, one-shot systemd unit** —
`cypherd-upgrade.service`, started by `cypherd-upgrade.path` when a request
file appears in a handoff directory — and the plane's entire power is to *ask*.
The unit runs the same binary under a different entry point
(`cypherd upgrade`), which keeps one artifact to sign and ship.

```
/var/lib/cypherpanel/upgrade/      0770 root:cypherpanel-upgrade
    request.json    written by the plane (0640), consumed and unlinked
    status.json     written by the helper (0644), read by the plane
    slots/          cypherd.new · cypherd.prev · the cached signed manifest
```

The service unit joins that group with `SupplementaryGroups=cypherpanel-upgrade`
and `ReadWritePaths=/var/lib/cypherpanel/upgrade`, which works regardless of
what UID `DynamicUser` picked this boot. Two lines in a unit, one directory,
one group: the whole increase in install surface, and it is a real cost against
vision.md's *"adding a tool, not adopting a platform"* — accepted because the
alternative is the operator doing the same steps by hand, less carefully, at
the moment they are least calm.

**Be exact about what this grants.** A compromised plane can now write a
request file and cause its host to install a **genuine, signature-verified
CypherPanel release** as root. That is strictly more than it could do before,
and it is stated here rather than discovered. What bounds it is the same bound
ADR-010 §3 gives the fleet: the helper verifies the artifact against the
release public key baked into its own binary and installs nothing else, so the
choice available to an attacker is *which authentic release*, not *what code*.
Downgrade is bounded further — the helper refuses a version below the running
one unless the request carries `rollback: true` **and** the target appears in
the host's own local slot history, so "walk the panel back to a release with a
known CVE" is not available for a version this host never ran. The helper
**opens `/etc/cypherpanel/cypherd.env` read-only and never writes anywhere
under `/etc`**: that file holds the master key, and destroying it is exactly
what coolify#3687 did. Threat-model §5.1 already requires the key to live
"outside the panel's own auto-update blast radius"; this is the sentence being
honoured, and the implementing PR adds a §5.16 scenario for the helper itself.

**Is the request file an imperative verb?** Hard rule 3 and ADR-005 require
features to be expressible as desired state, and this deliberately is not. The
rule governs the plane↔agent boundary — what should be true on a **Server** —
and the panel's own version is not on that boundary. More to the point, the
desired-state shape *is* the thing ADR-010 forbids: a `desired_panel_version`
the host converges on is an auto-install with extra steps. A one-shot request
that carries an actor, a nonce and a 30-minute expiry, and that a person
created, is the correct shape for a decision that is deliberately not
automatic.

**The compose install cannot be helped, and says so.** `deploy/docker-compose.yml`
runs the plane as a container with no Docker socket, and it is not getting one
(vision non-negotiable 5's neighbourhood, and a socket in the plane is
threat-model §5.2's privilege in the wrong place). `GET /panel/updates` reports
`mode: "assisted" | "manual"`; in `manual` mode the pre-flight still runs — it
is all reads — and the screen renders the two commands instead of a button,
the way the join command is already handed over as text. Version history still
records what ran (a boot whose version differs from the last recorded one
writes a row with an `external` actor), but offers no rollback and no
snapshots. A panel that pretends to a capability it does not have is worse than
one that hands over cleanly.

## 4. Pre-flight — before anything changes

`GET /api/v1/panel/updates/preflight?version=v1.1.0` runs five checks and
changes nothing, which is why it is a GET. Its answer is cached for 60 s so
re-rendering the screen does not re-download a manifest.

> **Update to v1.1.0?**
> ✓ signature verified (offline release key)
> ✓ disk headroom 41 GB — snapshot fits
> ✓ 3/3 agents compatible (≥ v1.0.2)
> ✓ fallback snapshot of v1.0.3 ready
> ⚠ 1 deploy running — waits until it finishes

**Signature.** Fetch `SHA256SUMS`, `SHA256SUMS.sig` and `release.json` for the
tag, verify the signature against the release public key compiled into this
binary, and check `release.json`'s digest against the manifest. One new file in
the release, covered by the manifest that is already signed — so a release
gains a machine-readable description with **no new signing machinery**:

```json
{ "version": "v1.1.0", "schema_version": 43, "rollback_floor": "v1.0.2",
  "agent_min_version": "v1.0.2", "notes_url": "…", "published_at": "…" }
```

The fetch uses the same client discipline as the update check (threat-model
§5.13): http(s) only, timeout, body cap, three redirects, none private. A
failure here is a refusal, never a warning — an unverifiable artifact is the
one thing this feature exists to not install.

**Disk.** The snapshot needs room for a dump of the panel database plus the new
binary. Required free is `pg_database_size() × 2 + CYPHERD_MIN_DISK_FREE`
(1 GiB, the floor `core/guard` already enforces at boot). ×2 rather than ×1
because a restore must be able to exist beside the thing it replaces. Below
that, the upgrade is refused with both numbers named — a disk that fills
*during* an upgrade is how the reference platforms produce a panel that is
neither the old version nor the new one.

**Agents.** `release.json`'s `agent_min_version` against every server's
observed `agent_version`, which heartbeats have carried since Phase 1. This is
not theoretical: per-agent NATS reply inboxes were a deliberate agent/plane
compatibility break, and there will be others. Servers below the floor are
**named**, with the remedy (raise the channel in
[agent-updates.md](agent-updates.md)'s screen, or re-run the installer). Below
the floor the upgrade is refused by default; proceeding requires typing the
target version in the confirmation, because orphaning the fleet's management
plane is close enough to irreversible to earn ui-principles §2's typed
confirm — and it stays *one* dialog, because confirmations never stack. An
agent that is merely offline neither blocks nor counts, for the reason
agent-updates §5 gives about the canary gate: a refusal an operator judges
irrelevant is a refusal they stop reading.

**Snapshot readiness.** Not the snapshot itself — that is taken at swap time
(§5) — but proof it can be taken: a working dump path and the space for it.

**Quiescence.** In-flight Deployments and DatabaseRestores are counted and
shown. This is a courtesy, not a correctness requirement, and the distinction
is worth stating because it is the architecture paying off: builds and restores
execute on **agents** (ADR-002, ADR-005), work items are persisted before they
are published (ENGINEERING 15), and `CYPHERD_HEARTBEAT_STALE` is 90 s against a
restart of a few seconds — so a plane that restarts mid-deploy does not
interrupt it. What the wait buys is attribution: a deploy that fails for an
unrelated reason during an upgrade will be blamed on the upgrade forever. So
the default is to wait, bounded by `CYPHERD_UPGRADE_QUIESCE` (15 min), after
which it proceeds and the status says it did — holding a read-only panel
hostage to one stuck build is the worse failure.

Under the checks, the retention control, which is the one decision the operator
makes and must not be made for them:

> **Keep old version** `7 days ▾` — 7 / 30 days · forever — you decide when (if
> ever) the fallback is pruned

and the promise that makes the whole thing tolerable:

> Panel is read-only for ~60s during the swap. **Your apps keep serving** —
> they don't depend on the plane.

That sentence is true because of ADR-005, and it is the single most important
piece of copy in this feature. Precisely: the panel is read-only for the whole
window, and **unreachable for the few seconds of restart and migration inside
it** — the progress screen says which of the two it is at any moment rather
than rounding both to "read-only".

## 5. The fallback snapshot

The snapshot is a dump of the panel's Postgres database, taken with `pg_dump`
in custom format by the helper, sealed with the master key, and written 0600 to
`<data dir>/snapshots/`.

**Why the helper and not the plane.** The plane cannot run `pg_dump`: the
container image does not carry it and the systemd unit forbids executing
anything outside its own paths. Writing a logical dump in Go over the pool
instead was considered and rejected — it means owning foreign-key ordering,
sequences, extensions and constraint deferral for a schema that grows every
release, to reimplement a tool that already handles all four. The helper
resolves a dump path in a fixed order: `CYPHERD_SNAPSHOT_PGDUMP` if set →
`pg_dump` on `PATH` whose major version is ≥ the server's → `docker exec` into
the container the `DATABASE_URL` host resolves to, which is the default install
(`cypherpanel-postgres`, created by `install.sh`). If none resolves, the
pre-flight's snapshot line is a **refusal**, not a warning.

The helper reads `CYPHERD_DATABASE_URL` and `CYPHERD_MASTER_KEY` from the same
root-owned `0600` env file the service unit already loads. Nothing sensitive is
ever written into `request.json`: the plane names a version and an intent, and
the helper fetches its own credentials from the file it is allowed to read and
forbidden to write.

**It is taken at swap time, not at pre-flight time.** A snapshot taken when the
operator opened the screen is stale by the time they press the button, and
everything written in between would be silently lost by a restore that claims
to rewind "to the moment of upgrade". The snapshot is taken after the read-only
lock is held, so nothing can be written after it that the restore would drop.

**The data directory is deliberately not in it.** JetStream's WORK stream and
the runtime-log spool live there, and ENGINEERING 15 makes them transient by
rule: anything that must survive a restart is in Postgres before it is
published. Snapshotting a message spool would capture in-flight work in a state
that no longer matches the database it is restored beside.

**The index-rebuild problem, which is real and easy to miss.** The record of
which snapshots exist lives in the database that the snapshots are *of*.
Restoring one therefore rewinds the snapshot table, erasing the row for every
snapshot taken since — including, possibly, the one an operator would want
next. So the **directory is the source of truth and the table is an index**:
each file carries a plaintext header (id, panel version, schema version,
created_at, retention, and the SHA-256 of the sealed body) ahead of the
encrypted payload, and the plane rebuilds the table from a directory scan on
every boot. The header is plaintext on purpose — a snapshot must be
identifiable by an operator who has lost the panel, and it names nothing
secret.

## 6. The swap, and the gate

Phases, each written to `status.json` by the helper and mirrored into
`panel_upgrades` by the plane whenever it is up:

```
preflight → waiting_for_quiesce → snapshotting → downloading → verifying
          → migrating → restarting → health_gate → succeeded
                                                 | rolled_back | failed
```

which the screen renders as the canvas wrote it — *snapshot v1.0.3 saved
(18 MB, encrypted)*, *artifact downloaded · checksum ✓*, *migrating database ·
3 of 4 migrations*, *restart · self health check (120s gate)* — under a heading
that says the only thing an operator needs to know about their browser:

> **Updating… don't close is not a thing — safe to leave.**

True by construction: the helper is a root process on the host with no
dependency on the session that asked, so closing the tab, losing the network or
signing out changes nothing.

**The read-only lock.** Before anything, the plane refuses mutating requests
with `503` and a `Retry-After`, reads and SSE continuing. It is the "update
lock against double-trigger" the pain-points row demands, and it is enforced
where a race cannot slip past it: a partial unique index over `panel_upgrades`
where `finished_at IS NULL` makes "at most one active upgrade" a database
invariant rather than a check in a handler. The lock carries an `expires_at`
(30 min); any request finding an expired one clears it, so a helper that dies
between phases cannot leave the panel read-only forever.

**Migration is its own step.** The helper runs `cypherd migrate` with the *new*
binary before starting the service, rather than letting boot-time migration do
it. Two reasons: a migration failure becomes attributable ("migration 42 failed",
not "the panel did not come back"), and the count is honest progress rather
than a spinner (ui-principles §3). Boot-time `store.Migrate` stays exactly as
it is and finds nothing to do.

**Two-slot swap**, the same shape the agents use (ADR-010 §4): stage into
`slots/cypherd.new` on the same filesystem as the target, `fsync`, run
`<staged> version` as a child and require it to print the expected version —
one `fork`/`exec` that eliminates the whole class of artifact that cannot reach
`main()` at all — then `cypherd → cypherd.prev`, `new → cypherd`,
`systemctl restart cypherd`. The signed manifest for the *installed* version is
cached beside the slot; §8 explains what that buys.

**The gate.** `CYPHERD_UPGRADE_PROBATION` (120 s): `/readyz` must answer 200 —
it pings the database, so it proves boot, migration and the pool together — and
the unit must not have restarted during the window (`systemctl show -p
NRestarts`). Both, because a crash-looping service answers 200 in between
crashes.

**Auto-rollback, in the order that loses least.** On a failed gate the helper:

1. renames `cypherd.prev` back and restarts the service. If the old binary
   comes up, **stop here** — this is the common case, nothing is lost, and
   everything written on the new version in the last minute is still there;
2. only if the old binary cannot come up — because the schema moved past what
   it can read (§7) or a migration stopped half-way — restores the snapshot
   into the database and starts the old binary again;
3. and if *that* fails, stops, leaves both the snapshot and the failure detail
   in place, and writes what it knows to `status.json`. The panel is down; a
   helper that keeps trying things at this point is a helper making an incident
   worse.

Ordering matters and is the whole point: the cheap, non-destructive path is
attempted first and the destructive one is a fallback, never the routine.

**The canvas copy needs one correction.** It reads *"it rolls itself back to
the snapshot automatically"*. It does not, and should not: reaching for the
snapshot first would throw away real work to fix a problem a binary swap
usually fixes for free. The shipped copy is *"If v1.1.0 fails its own health
check within 120s, it rolls itself back automatically — the same two-slot swap
the agents use (ADR-010)."*

**What the gate does not prove.** That the panel *works*. A build that boots,
migrates, answers `/readyz` and then serves a broken deploy pipeline passes
every check here, and no self-check closes that — reaching readiness is the
only thing a process can honestly assert about itself. This is the same
residual [agent-updates.md](agent-updates.md) §4(d) records for the fleet, and
the answer is the same: the remedy is an explicit rollback by a person, which
is what §7 and §8 exist for. The undo window is the feature; the gate only
catches the failures that are obvious.

## 7. The hardest part: a rollback is only real if the schema lets it be

Everything above is straightforward next to this. A binary swap is easy; a
*schema* does not swap. Two candidate designs, and the one that survives is the
less obvious one.

**Rejected: run the down migrations.** Every migration in the tree carries a
`-- +goose Down`, ENGINEERING 16 requires it, and the canvas even promises
*"v1.1.0's schema changes are reversed"*. It is the wrong mechanism, and the
canvas contradicts itself one sentence later. The down of *"create table
threshold_alerts"* is *"drop table threshold_alerts"* — which destroys exactly
the rows the operator created while running v1.1.0. A rollback that deletes the
alerts you configured this morning is not a rollback; it is a partial restore
that took the long way round. And the canvas's own next promise — *"Data for
features v1.0.3 doesn't have is kept dormant and comes back if you
re-upgrade"* — is only achievable if the schema is **not** reversed. So the
promise is kept and the mechanism is dropped.

**Accepted: the schema moves forward only, and the previous binary must be able
to run against it.** Rolling back swaps the binary and nothing else. The new
tables and columns stay; the old binary never selects them, so they sit
dormant, and a later re-upgrade finds them exactly as they were.

That is only true if migrations are *backward compatible with the previous
release*, which is a stronger property than "reversible" and does not follow
from it. ENGINEERING 16's additive-first discipline gets most of the way there
and is the reason this is affordable at all. What breaks it is specific and
enumerable: a `NOT NULL` column with no default (the old binary's inserts name
no such column), a dropped or renamed column (sqlc generates explicit column
lists, so the old binary's `SELECT` fails immediately), a narrowed `CHECK` or
enum the old binary still writes, a type change.

So the property is **declared, checked, and proved**:

- **Declared.** `release.json` carries `rollback_floor` — the oldest panel
  version that can run against this release's schema.
- **Checked.** A migration that breaks backward compatibility must carry
  `-- cypher:breaks-rollback` in its header, and a lint fails the build when
  such a migration lands without the floor being raised in the same PR. Two
  places to state one fact, deliberately, so neither can drift silently.
- **Proved.** A CI job — real Postgres, per ENGINEERING 29 — migrates a
  database to the new schema, then boots the `rollback_floor` release's binary
  against it and runs that release's store tests. If it fails, the floor is
  wrong. This is the job that turns a comment into a guarantee, and it is the
  most valuable single thing in this spec.

**When a release genuinely cannot be rolled back**, that is not hidden. The
pre-flight says so before anything changes — *"⚠ v1.1.0 cannot be rolled back
by swapping the binary. The only way back is restoring the snapshot, which
discards everything done after the upgrade."* — and the version-history row
renders `Roll back` disabled with that reason attached. An operator who learns
this at pre-flight schedules a window; an operator who learns it at rollback
time has already lost the argument.

**The residual, stated plainly.** This makes rollback real for the ordinary
release and honest about the extraordinary one. It does not make every release
rollback-safe, and no design can: some schema changes genuinely cannot be read
by older code. What it removes is the *surprise*.

## 8. Version history, retention, and the last resort

`Settings → Updates → Version history` — under Settings, where the panel's own
lifecycle lives, while the fleet's version screen is a tab on Servers. Neither
is a fifth nav item (ui-principles §4).

```
v1.1.0   CURRENT     installed today 03:52 · by sam · healthy since
v1.0.3               snapshot 18 MB · kept 7 days (your choice) ·
                     expires in 6d 22h · extend ▾            [↺ Roll back]
v1.0.2               snapshot 17 MB · kept forever (pinned ∞ — your choice)
                                                              [↺ Roll back]
v1.0.1               kept 7 days, pruned Jul 19 · auto-rolled-back once on
                     Jul 12 (health gate) — the system noted why   expired
```

Two things the layout is saying that are worth making explicit. The v1.0.1 row
**outlives its snapshot** — the file was pruned, the history was not, and the
one auto-rollback this panel ever performed is still on the record with its
reason. And the two `Roll back` buttons are styled differently on purpose:
v1.0.3 is the local `.prev` slot and is immediate, while v1.0.2 must be
re-downloaded and re-verified first. The screen shows the difference rather
than hiding a 30-second download behind an identical button.

**One previous binary slot**, for ADR-010 §6's reason: a chain of old binaries
is a version history nobody audits and a per-host disk cost on every update,
and any older version is one the plane can name and fetch.

**Rolling back to the slot works offline**, which is a deliberate divergence
from agent-updates §6, where a downgrade re-downloads even though `.prev`
usually holds the artifact. That rule is right for a fleet host nobody is
watching. Here the reason to roll back is often that something is wrong, and
requiring egress at that moment is a bad trade — so the swap caches the
*installed* version's signed manifest beside its slot, and a rollback verifies
`cypherd.prev` against that cached signature. Offline, and still signature-
checked; the divergence buys availability without giving up the property.

The confirmation is the copy, minus the one clause §7 corrected:

> **Roll back to v1.0.3?**
> **Nothing you created is lost — in either direction.** The binary swaps back;
> v1.1.0's schema stays in place and v1.0.3 simply does not use it, so all data
> stays: the user you invited yesterday on v1.1.0 still exists on v1.0.3. Data
> for features v1.0.3 doesn't have (e.g. threshold alerts) is kept dormant and
> comes back if you re-upgrade. Apps, databases and deploy history on your
> servers were never involved. ~60s; agents re-sync on heartbeat.
> `↺ Roll back, keep my data`  `Cancel`

*"Agents re-sync on heartbeat"* needs no mechanism: they are dialling home
already, and the plane's absence for a minute is far inside
`CYPHERD_HEARTBEAT_STALE`.

**Retention is the owner's call**, made at update time and changeable
afterwards: 7 days, 30 days, or forever, extendable or pinnable from this
screen. An hourly sweeper prunes expired snapshots in bounded batches, the same
owned-goroutine shape the audit-retention sweeper uses. A cap
(`CYPHERD_PANEL_SNAPSHOT_MAX`, 5) applies **only to unpinned snapshots** — a
cap that silently deleted something the operator pinned "forever" would break
the one promise this screen makes. Pinned snapshots are never auto-removed;
their total size is shown, and it counts toward the ordinary
`CYPHERD_DISK_WARN_PERCENT` alarm like any other bytes on that disk.

**The last resort is separated, and looks it:**

> Last resort — panel database corrupted? **Restore the snapshot instead**:
> rewinds panel state to the moment of upgrade (typed confirm, audit-logged).
> That's the only path that discards anything.

It sits below a rule, in the destructive voice, behind a typed confirmation of
the panel version (ui-principles §2), and it names the loss instead of
softening it. It is the only operation in this feature that throws away work,
and the screen's job is to make sure nobody arrives at it by momentum.

## 9. The changelog

The help menu's dot opens `What's new` (`Settings → Updates`, same route,
different pane): a timeline of versions with one-line summaries, `you're on
v1.0.3` in the corner, an `AVAILABLE` chip on the newer one. *"Surfaces as one
quiet dot on the help menu after an update — never a modal takeover."*

The content is **embedded in the binary** from a repository `CHANGELOG.md` via
`go:embed`, not fetched. Fetching a list of releases would mean a second
outbound call with its own rate limit, and it would render prose from a network
source inside an operator-facing surface — the injection concern threat-model
§5.13 raises about the tag and notes URL, multiplied by a page of Markdown.
Embedding costs a `CHANGELOG.md` that does not exist yet and must be maintained
per release (it joins the release checklist beside signing); it buys a changelog
that works air-gapped and cannot be written by anyone who compromises a feed.
The *available* version's entry shows only what the signed `release.json` and
the feed already give — version, kind, and `Release notes ↗` pointing out — so
no untrusted prose is ever rendered.

This also lets the `panel.update_available` inbox item finally carry a link,
which control-plane-hardening §3 explicitly deferred: *"there is no in-panel
changelog route yet"*.

## 10. API, authorization and audit

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/panel/updates` | member, read | running build, `latest`, `mode`, the active upgrade if any |
| `GET /api/v1/panel/updates/preflight` | **owner, session-only** | `?version=`; the five checks; changes nothing |
| `POST /api/v1/panel/updates/upgrade` | **owner, session-only** | version, snapshot retention, acknowledgements; `409` if one is active |
| `POST /api/v1/panel/updates/cancel` | **owner, session-only** | only before the swap; `409` after |
| `POST /api/v1/panel/updates/rollback` | **owner, session-only** | target version; `409` when below the schema's `rollback_floor`, naming it |
| `GET /api/v1/panel/updates/history` | **owner, session-only** | upgrades joined to snapshots |
| `PATCH /api/v1/panel/snapshots/{id}` | **owner, session-only** | retention: extend, pin, expire |
| `DELETE /api/v1/panel/snapshots/{id}` | **owner, session-only** | |
| `POST /api/v1/panel/snapshots/{id}/restore` | **owner, session-only** | the last resort; typed confirm |
| `GET /api/v1/panel/changelog` | any authenticated | embedded entries + the available release |

**Owner, and session-only**, on everything that acts. This is the control that
decides what code the control plane runs; an API token that can move it is an
API token that owns the panel, and API tokens live in CI. It is the rule
[deploy-protection.md](deploy-protection.md) set for break glass and
[agent-updates.md](agent-updates.md) applies to channels, and it matters more
here than in either.

Live progress rides the existing SSE stream while the plane is up, so the
screen does not poll. During the restart the browser gets connection refused
and shows *"panel restarting…"*, retrying until `GET /panel/updates` answers
again with the full timeline the helper recorded meanwhile. **The one thing the
panel cannot narrate is its own absence**, and pretending otherwise — a fake
progress bar advancing while nothing is being observed — is exactly the
"mystery spinner" vision.md rules out.

`panel.upgrade_started`, `panel.upgrade_succeeded`, `panel.rolled_back`,
`panel.snapshot_restored`, `panel.snapshot_pinned` and
`panel.snapshot_deleted` join the audit vocabulary; a rollback the *helper*
performed is attributed to the `system` actor with the failure in the detail,
the way an automatic preview-environment reclaim already is. Two panel-level
inbox kinds join beside `panel.update_available`: `panel.upgraded` and
`panel.rollback_performed` — the second especially, because an automatic
rollback at 03:00 that nobody is told about is a panel quietly running an older
version than its owner believes. *"Updates, rollbacks and snapshot restores are
all audit-logged and land in the inbox."*

The glossary gains three terms before any of this copy ships (hard rule 5):
**Panel Snapshot** — a sealed dump of the panel's own database taken at the
moment of an upgrade, whose retention its owner chooses; **Upgrade** — one
recorded transition of the control plane from one release to another, the
panel's own analogue of a Deployment; and **Rollback Floor** — the oldest panel
version that can run against a release's schema, declared by that release and
proved in CI.

## 11. Data model

Migration `0043_panel_updates.sql` — the highest on disk is `0039_server_disk.sql`
and `0040`–`0042` are claimed by the specs written alongside this one
(agent-updates, metrics-and-usage, app-scaling, threshold-alerts), so the
implementing PR takes whatever is actually next.

```sql
panel_upgrades
  id, from_version, to_version, actor_id, actor_label,
  phase, detail, outcome,          -- succeeded | rolled_back | failed
  snapshot_id, lock_expires_at,
  started_at, finished_at
  -- at most one active upgrade, enforced by the database
  CREATE UNIQUE INDEX panel_upgrades_active ON panel_upgrades ((true))
      WHERE finished_at IS NULL;

panel_snapshots                     -- an INDEX; the directory is the truth (§5)
  id, panel_version, schema_version, path, size_bytes, sha256,
  created_at, expires_at NULL,      -- NULL = pinned forever
  pruned_at NULL, state             -- present | pruned | missing

users
  update_banner_dismissed_version TEXT NOT NULL DEFAULT ''
```

`panel_upgrades` carries no foreign key to `users`, for the reason
[audit-log.md](audit-log.md) established: a record of who upgraded the panel
must outlive the account that did it. `snapshot_id` is a plain column, not a
reference — the row survives the file.

## 12. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_UPGRADE_PROBATION` | `120s` | How long the new build has to hold `/readyz` before the helper rolls back (§6). |
| `CYPHERD_UPGRADE_QUIESCE` | `15m` | How long to wait for in-flight deployments before proceeding anyway (§4). |
| `CYPHERD_PANEL_SNAPSHOT_MAX` | `5` | Cap on **unpinned** snapshots; the oldest expiring one is pruned first. |
| `CYPHERD_SNAPSHOT_PGDUMP` | unset | Explicit `pg_dump` path, first in the resolution order (§5). |
| `CYPHERD_UPGRADE_MODE` | auto | `manual` forces the hand-over shape on a host where the helper is installed but should not be used. |

`CYPHERD_UPDATE_CHECK=off` already disables the outbound check; with it off
there is no banner and no available version, and the upgrade screen offers
only a version typed in by hand — which still runs the full pre-flight,
because an air-gapped panel deserves the signature check most of all.

## 13. Deliberately out of scope

- **Automatic panel updates, on any trigger.** ADR-010 §6: the plane never
  updates itself. No channel, no timer, no "install patch releases
  automatically" checkbox. A patch that is safe enough to install unattended is
  indistinguishable, in advance, from the one that takes the panel down at 03:00
  with nobody watching — and the reference platforms' trust wound is precisely
  that promise made and broken.
- **Scheduled or off-site panel backups.** The snapshot here is a fallback for
  one upgrade, kept locally, on the same disk as the panel. Losing the panel
  host loses it too. Nightly encrypted plane snapshots to a Backup Target, and
  `cypherd restore --from s3://…`, are disaster recovery — a separate feature
  with its own spec, which should adopt this snapshot format rather than invent
  a second one.
- **Rollback by reversing migrations.** §7. The downs stay in the tree for
  development, where dropping a table you created ten minutes ago is exactly
  what you want; they are never run against an operator's data.
- **Upgrading Postgres.** The panel manages its own binary. The database
  underneath it is the operator's, and a major-version upgrade of it is not
  something to trigger from a web button.
- **Guided upgrade on the container install.** §3: no Docker socket in the
  plane, so pre-flight and hand-over is what that path gets. Reconsidering this
  means an ADR about what the control plane is allowed to hold, not a flag.
- **Multi-node or HA panel upgrades.** vision.md's explicit non-scope: one
  control plane, many workers. There is nothing to roll across.
- **A "check for updates now" button.** The checker polls every six hours and
  caches; a manual re-check is an outbound request an operator can trigger in a
  loop, and it answers a question the screen already answers. Typing a version
  into the upgrade form is the escape hatch for the impatient.
- **Streaming the helper's log to the panel.** During the one window that
  matters the panel is not running. The phase, the detail and
  `journalctl -u cypherd-upgrade` are the record — the same trade
  agent-updates §10 makes for the same reason.
- **Beta or pre-release channels for the panel.** The update check ignores
  drafts and pre-releases (threat-model §5.13), and a panel-side channel
  selector would be a second, weaker version of the fleet's channel model for
  a population of one host. An operator who wants a pre-release types its
  version.
