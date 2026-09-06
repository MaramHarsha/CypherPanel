# Feature spec: Agent version channels and controlled rollout

> The implementation of [ADR-010](../adrs/ADR-010-agent-auto-update.md): a
> **desired agent version** per server, chosen by a **channel**, converged by
> the agent itself against a signed artifact it fetches over its own outbound
> HTTPS. The control plane never updates itself and never carries a binary.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. What ADR-010 left open

ADR-010 is Accepted and is not re-litigated. It fixes the mechanism: desired
version + checksum on a channel, the agent downloads the artifact itself,
signature over trust-the-plane, two-slot swap, self-rollback when the new
binary cannot reach its first heartbeat, rollout by channel order with an
operator promoting canary to stable, and the plane never updating itself.

What it left open is everything that decides whether this is safe to switch
on: where the checksum comes from, given that the plane must not become a
distributor (§3); what happens when the new binary cannot run the rollback
check at all (§4); what the canary gate refuses (§5); how a downgrade is told
apart from an accident (§6).

Two pieces exist already. `Heartbeat.agent_version` has shipped since Phase 1
and is stored on the Server and on its DTO, so the *observed* half is live —
drift is visible today, it just has nothing to converge on. And the join
command already pins `CYPHER_AGENT_URL` to a release asset on a release panel
(`core/updates.AgentAssetURL`), so a fresh host lands on a named version.

One hard dependency: [release-signing.md](../dev/release-signing.md) records
`RELEASE_PUBKEY` as *not yet generated* and agent-side verification as
unimplemented. Shipping the updater first would ship it with the signature
check stubbed — the one thing ADR-010 §3 forbids. This is what "lands with the
release pipeline" means in practice.

## 2. Two channels, one selector per server

The obvious model is a desired version per server. It is also the wrong thing
to put in front of an operator: a forty-host fleet becomes forty decisions that
must agree, and "promote" becomes a bulk edit. So the plane holds **two rows**
and a server holds **which row it follows**:

```
agent_channels
  channel          TEXT PK   -- 'stable' | 'canary'
  desired_version  TEXT NOT NULL DEFAULT ''
  artifact_base    TEXT NOT NULL DEFAULT ''  -- '' = derive from the version
  rollback         BOOLEAN NOT NULL DEFAULT false    -- §6
  updated_at, updated_by

servers
  agent_channel        TEXT NOT NULL DEFAULT 'stable'
  agent_update_phase   TEXT NOT NULL DEFAULT ''   -- observed, from heartbeats
  agent_update_target  TEXT NOT NULL DEFAULT ''
  agent_update_detail  TEXT NOT NULL DEFAULT ''
```

Migrations top out at `0039_server_disk.sql`, so `0040` is free in the
codebase — but it is claimed on paper by app-scaling, metrics-and-usage,
log-drains, app-access-control and revision-promotion, none of them
implemented. Whichever ships first takes the number and the implementing PR
takes whatever is actually next; nothing here depends on which.

A server's desired version is its channel's. Promotion is one write, joining
canary one dropdown, and what crosses the wire is still per-server desired
state — which keeps ADR-010 §6's "staged rollout is a Later refinement on the
same primitive" true rather than aspirational.

**Both rows ship empty, and empty means no instruction.** Upgrading a panel
must not start replacing binaries across a fleet nobody asked it to touch —
that is the exact trust wound the [feature matrix](../product/feature-matrix.md)
records against Coolify, "bricked panels, lost encryption keys". That row is
about auto-update of the *platform*, which §10 puts out of scope; the wound is
borrowed by analogy, because it is the same shape pointed at forty hosts rather
than one. Defaulting the field to the panel's own version would inherit it on
purpose. (The row is also stale where it credits ADR-010 with a "pre-update
snapshot, atomic apply + health-verified rollback, update lock" — ADR-010 §6
says the plane never updates itself. Nothing here inherits that scope.)
The cost is real: a fleet whose operator never opens this screen stays stale,
which is the burden ADR-010 exists to end. Two things pay for it. The empty
state *is* the action (ui-principles §11) — the fleet's current versions and
one button, `Match the panel (v1.0.3)`. And when running versions drift from
the panel's own build for more than a day, one inbox item says so, once.

**The plane refuses a desired version newer than its own build**, naming the
remedy: update the panel first. Additive-only proto (ENGINEERING 18) guarantees
the old-agent direction, not the new-agent one. A development build cannot make
that comparison and so does not — it warns and allows. Only half of that is
borrowed: `core/updates.IsRelease` is the shared judgement of what counts as a
release, and the join command already makes it. But the join command's
dev-build behaviour is *silence*, not a warning — `AgentAssetURL` returns `""`
and `installCommand` simply omits `CYPHER_AGENT_URL`
(`core/api/rest/handlers_servers.go`), which its test asserts by absence. The
warning is new here, and it is warranted by the difference in the act: an
operator pasting a join line is not choosing a version, and an operator setting
one for the fleet is.

## 3. The update, in order

The updater is a reconciler like any other: given desired state, converge and
report. Its convergence happens to end with the process exiting. On sync, and
on the `resync` nudge the plane already publishes for node-wide changes, it
compares the spec's version with its own build stamp; equal or empty is zero
work, which is the common case forever.

When they differ:

1. **Wait for the host to go quiet.** Stop fetching work items, let the one in
   flight finish. A restart mid-build throws away ten minutes of CPU; a restart
   mid-restore interrupts a database that is already offline. Redelivery would
   recover both — but recovering is not the same as not breaking it, and
   desired state has no deadline. A random 0–`CYPHER_UPDATE_JITTER` delay
   (60 s) sits in front, so a promotion does not restart forty agents at once.
2. **Fetch and verify the manifest.** `SHA256SUMS` and `SHA256SUMS.sig`, then
   `ed25519.Verify` against the key baked into this binary. This is where
   ADR-010's checksum resolves: **the signed manifest is the checksum**, and it
   is signed, which a digest copied into the plane's database is not. A
   plane-declared digest would be a second, weaker source of the same fact —
   and one a compromised plane controls. The fetch is bounded the way
   threat-model §5.13 bounds the plane's update check — http(s) only, a
   timeout, a body cap, at most three redirects — but deliberately **not** by
   §5.13's refusal of private addresses, which is a plane-side control and does
   not transplant. An air-gapped fleet's mirror (`artifact_base`, §7) lives at
   a private address by definition; refusing it agent-side would break the case
   this spec exists to allow. What stands in its place is the signature: the
   agent fetches from wherever the operator points it and runs nothing a
   baked-in key does not verify, which is a stronger bound than a destination
   check and is the only one that survives a mirror.

   That discipline is re-implemented, not reused, and which one it is matters
   to whoever picks the work up. The caps and the
   redirect guard live in the core module (`ErrPrivateRedirect`,
   `ErrBodyTooLarge`, `maxRedirects`, `maxBodyBytes` — `core/updates/updates.go`)
   and `go.work` declares `./agent`, `./core` and `./pkg` as three separate
   modules, so `agent/` cannot import `core/updates`. The bounded client
   therefore goes in `pkg/`, where code both sides may hold already lives
   (`pkg/subjects`, `pkg/registryauth`), with the agent as its first caller.
3. **Download `cypher-agent-linux-<arch>`** for the agent's own `GOARCH` — the
   plane never guesses the host's architecture, because it has never needed to
   know it — and check its SHA-256 against the manifest. `fsync` before
   anything is renamed: a power cut must not leave a zero-length file where the
   binary was.
4. **Pre-flight it.** Run the staged file as a child: `<staged> version` must
   exit 0 printing the version desired state named. One `fork`/`exec`, and it
   removes the whole class of artifacts that cannot run at all — wrong
   architecture, truncated download, an HTML error page served with a 200. That
   matters more than it looks: a binary that cannot reach `main()` cannot roll
   *itself* back, so the only place to catch it is before the swap, in a
   process that still works. (`agent.sh` already greps for `ELF` for the cheap
   half of this reason.)
5. **Two-slot swap.** Stage into `.cypher-agent.new` **beside the running
   binary**, never in the state dir: `rename(2)` is atomic only within a
   filesystem, and `/var/lib` is usually a different mount from
   `/usr/local/bin`. Then current → `.prev`, `.new` → current. The state
   directory is untouched, so identity and certificate survive by not being in
   the swap's path at all. The write-temp/`fsync`/`os.Rename` shape is not
   invented here: `agent/identity` already does exactly it for the certificate
   swap (`agent/identity/identity.go`, `agent/identity/renew.go`), with tests
   that assert the atomicity property rather than the call sequence. The
   updater follows that code, and its tests follow those tests.
6. **Write the boot marker** — target, previous, attempt count, probation
   deadline — and `os.Exit(0)`. The installer's unit is `Restart=always`,
   `RestartSec=5`.

Workloads do not notice. Containers keep running, the Proxy keeps serving, and
`CYPHERD_HEARTBEAT_STALE` is 90 s against a gap of about eight — a clean update
does not even flicker the server's status. Only the RUNNING column changes.

Converging twice equals converging once (ENGINEERING 13), and here that is
load-bearing rather than a formality, because the second convergence is
performed by a *different binary*: it reads the same desired state, finds its
version equal, and does nothing. The idempotency test asserts exactly that —
second pass, no download, no rename.

## 4. The four ways it fails

ADR-010 §5's promise is worth keeping precise: *an update can strand a host at
the old version, never off the bus.* It holds by construction for three of
four classes.

**(a) The artifact is wrong or missing** — a 404 on an unpublished tag, a
signature that does not verify, a digest mismatch. Nothing has been renamed, so
nothing is at risk: report `failed` with the reason, back off, retry, stay
connected. The plane also `HEAD`s the manifest when an operator *sets* a
version, so a typo is refused where it is cheap rather than discovered by forty
hosts (`CYPHERD_AGENT_UPDATE_PRECHECK=off` for a source only agents can reach).

That precheck is the plane connecting to a host named in a request body and
returning the outcome synchronously — threat-model §5.14's registry probe
exactly, where a 200 against a refusal against a timeout separates a listening
port from a closed one from a filtered one inside the panel's network. So it
takes §5.14's controls and not the agent's: `artifact_base` is held to http(s)
with a checked host, and a resolved name or redirect target that is loopback,
private, link-local, unspecified or multicast is refused — §5.13's
`ErrPrivateRedirect` posture, which is the plane's and stays the plane's. A
`HEAD` reads no body, the timeout is short, and the route is owner-only, so the
probe is reachable only by a rank that already owns the fleet. Which is also
what the switch is for: a private mirror is precisely what that guard must
refuse, and `CYPHERD_AGENT_UPDATE_PRECHECK=off` is how an operator says "the
agents can reach it and you cannot" — rather than the plane relaxing a control
because a request body asked it to.

**(b) The new binary cannot run** — caught at step 4, before the swap.

**(c) The new binary runs but cannot dial home.** The case this design is for.
The new process finds the marker, increments the attempt count, and arms a
probation timer as the **first thing it does**, before loading its identity and
before any network I/O. It crashes, and the next start finds `attempts >= 1`
and rolls back; or it hangs, and at `CYPHER_UPDATE_PROBATION` (120 s) the timer
fires. Rolling back is `rename .prev → current` and `os.Exit(0)`: no network,
no plane, no decision. **The rollback path must never need the control plane,
because the failure it survives is "cannot reach the control plane."** The old
binary returns five seconds later and reports `rolled_back` with the failed
version in the detail — the screen's amber row.

The attempt limit is 1, not 3, and the unit file is why:
`StartLimitIntervalSec=60`, `StartLimitBurst=5`. At `RestartSec=5` a
crash-looping binary burns those five starts in twenty seconds and systemd puts
the unit in `failed` — permanently off the bus, the outcome forbidden. One
failed attempt plus one rollback plus the old binary is three starts in fifteen
seconds. "Try it a few times, it might settle" spends the budget the rollback
needs.

The marker clears when the agent has established the mTLS bus connection and
published its first heartbeat — deliberately **not** when the desired-state
sync answers. ADR-005 requires the plane to answer nothing rather than answer a
partial set, so a plane briefly unable to assemble desired state would look, to
every agent at once, exactly like a bad binary, and would roll back a perfectly
good release fleet-wide. Dial-home is the signal; convergence is not.

**(d) It dials home fine and is broken some other way.** It heartbeats, clears
the marker, and its Docker driver does not work. Self-rollback will not fire
and cannot: reaching the plane is the only thing a process can honestly assert
about itself, so the panel will show `✓ CONVERGED` for a host that is not fine.
This is the real cost of the design. It is what canary is *for* (§5), and the
remedy is an explicit rollback (§6). The promise is "never off the bus", not
"never broken".

One residual beyond the four: a binary that passes pre-flight, starts, then
wedges the runtime hard enough that its own probation timer never fires leaves
that host on the new version and off the bus, needing console access. The
window is narrow — the timer is armed in the first statements of `main`, code
every release runs and pre-flight exercises — but not zero, and no agent-side
cleverness closes it, because the agent is the thing that failed. Naming it
beats a watchdog that would need its own watchdog.

## 5. The canary gate

`canary` is opt-in per server, `stable` is the default, so a fleet with nothing
on canary has one channel and no gate. Promotion —
`POST /panel/agent-updates/promote` — makes stable's desired version canary's.
One button, one write: "Promote canary → stable".

It is refused, `409`, when **no canary server has converged on the candidate**
(promoting a version no host has run defeats a gate whose whole purpose is that
a host ran it — the message names what the canary servers are running), or when
**any canary server reports it `rolled_back`**. There is no override flag: an
operator who believes a rollback was spurious sets stable's version directly
and owns that explicitly.

A canary server that is merely *offline* neither blocks nor counts. Blocking
would make one powered-down host a permanent hold on every fleet update, and an
operator who learns the gate refuses for reasons unrelated to the release is an
operator who stops reading refusals.

**No automatic promotion after N minutes of healthy**, which is the first thing
anyone asks for. The only available health signal is the one §4(d) showed is
insufficient. A timer that promotes on heartbeats converts one bad release into
a fleet-wide outage while nobody is watching — strictly worse than the stale
fleet ADR-010 set out to fix. The gate is a person because the judgement is.

**Promotion moves every stable host at once.** No waves, no percentages;
ADR-010 §6 names staged rollout as Later on this same primitive and this spec
does not smuggle it in. The confirmation says so, with the count.

## 6. Downgrade, rollback, and rotating the key

The agent refuses a version below its running one (ADR-010 §3) unless the
channel's `rollback` flag is set; setting an older version sets it, behind a
confirmation that states the blast radius (ui-principles §2), and moving
forward clears it.

Be precise about what that buys. It does **not** stop a compromised plane from
pinning the fleet to a known-bad release — a plane that sets the version sets
the flag. It stops an *accident*: a stale desired set, a mistyped tag, a
restored database snapshot walking the fleet backwards. The bound against a
hostile plane is the signature, and it is the bound threat-model §5.1 already
states — it can choose among genuine releases and nothing else. Same honesty
[audit-log.md](audit-log.md) applies to itself: forensic, not preventive.

Operator-driven rollback is therefore ordinary and is one write. Agents
downgrade through the identical mechanism with the identical signature check;
the artifact is downloaded again even though `.prev` usually holds it, because
a rollback that trusted whatever `.prev` happened to contain would be a
rollback that skipped verification.

Exactly **one** previous slot is kept. A chain of old binaries is a version
history nobody audits and a per-host disk cost on every update; one slot is
what a rollback needs, and any older version is one the plane can name.

**Key rotation needs no side channel**, as ADR-010 promises, and the mechanism
is a list rather than a constant: the agent accepts an artifact any of its one
or two baked-in keys verifies. Release N is signed with A and bakes in {A, B};
N+1 is signed with B; N+2 bakes in {B} alone. Every step is an ordinary signed
update, and at most two keys are trusted at once — a list that only grows is a
trust surface that only grows. The keys are compiled-in defaults overridable by
`-ldflags`, which is also how an air-gapped fleet bakes its own. **There is no
flag that skips verification**: an operator running an internal mirror signs
their builds and bakes their key, or does not get automatic updates. A skip
flag is the hole everything else here leaks through.

The baked-in key protects a *running* agent from being handed a forged
artifact. It says nothing about the binary installed by hand in the first
place — that is what release-signing.md's manual verification is for.

## 7. Wire, API and authorization

**Additive, and no new subjects.** `DesiredState` gains field 6, `Heartbeat`
field 9; nothing is renumbered (ENGINEERING 18). A version change propagates on
the existing `work.<server>.resync` nudge, whose contract is already "re-read
your desired state" — so `pkg/subjects` is untouched (ENGINEERING 14) and
`subjects.Resync(serverID)` is already the per-server subject. What does not
exist yet is a per-server *publisher*. The plane only nudges the whole fleet
today: `Scheduler.RequestResync` walks every enrolled server
(`core/scheduler/scheduler.go`) and its one caller is `core/paneltls`. A
channel promotion can use it as it stands — it moves every host on that channel
anyway — while a single server changing its own channel (the `server.updated`
path below) needs the one-server sibling beside it, publishing the same
`ResyncWork` to one subject. Small, but an addition rather than something
already there.

```proto
// DesiredState field 6
message AgentUpdateSpec {
  string version = 1;        // '' = no instruction; never means downgrade
  string artifact_base = 2;  // prefix holding cypher-agent-linux-<arch>,
                             // SHA256SUMS and SHA256SUMS.sig
  bool rollback = 3;         // permits a version below the running one
}

// Heartbeat field 9
message AgentUpdateStatus {
  enum Phase {
    PHASE_UNSPECIFIED = 0; PHASE_IDLE = 1; PHASE_PENDING = 2;
    PHASE_DOWNLOADING = 3; PHASE_VERIFYING = 4; PHASE_SWAPPING = 5;
    PHASE_ROLLED_BACK = 6; PHASE_FAILED = 7; PHASE_DISABLED = 8;
  }
  Phase phase = 1;
  string target_version = 2;
  string previous_version = 3;
  string detail = 4;         // never a secret (rule 20)
}
```

One `artifact_base` rather than three URLs, because the three file names are
fixed by the release layout: a mirror mirrors the prefix. Empty derives it from
the version, which is every fleet using the project's releases — the plane names
a version and never stores or serves a byte of the binary (ADR-010 §2).

`PHASE_ROLLED_BACK` also raises the agent's self-reported `AgentStatus` to
degraded, and the plane already maps `AGENT_STATUS_DEGRADED` onto
`domain.StatusDegraded` (`core/status/status.go`), so the server goes amber in
the ordinary vocabulary — and not only in this feature's column — with no
plane-side change at all. The change is on the agent:
`agent/heartbeat.Health` (`agent/heartbeat/heartbeat.go`) today holds a single
`error` for the Proxy, so it becomes keyed by subsystem and two reporters
cannot clobber each other. That is a signature change to `Set`/`Err` with
exactly one existing wiring line behind it —
`dockerDrv.OnProxyHealth(health.Set)` in `agent/cmd/cypher-agent/main.go` —
plus `heartbeat_test.go`.

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/panel/agent-updates` | member, `read` | both channels, the fleet's version histogram, the resolved artifact base |
| `PUT /api/v1/panel/agent-updates/{channel}` | **owner, session-only** | `409` if newer than the panel; pre-flights the manifest |
| `POST /api/v1/panel/agent-updates/promote` | **owner, session-only** | `409` with names when the gate refuses (§5) |
| `PATCH /api/v1/servers/{id}` | admin, token-reachable — **`agent_channel`: owner, session-only** | the rank is checked per FIELD, not per route; `public_address` unchanged |

Owner, and session-only: this is the one control in the panel that changes what
code runs on every server, and an API token that can move a channel is an API
token that owns the fleet — API tokens live in CI. It is the rule
[deploy-protection.md](deploy-protection.md) set for break glass, applied where
it matters most.

The last row is the exception, and it is a field-level check for a reason.
`PATCH /api/v1/servers/{id}` is panel ADMIN today
(`a.requirePanelRole(w, user, domain.RoleAdmin)`,
`core/api/rest/handlers_servers.go`) and is deliberately reachable by an API
token: it is listed in `serverRoutes` in `core/api/rest/rest.go`, whose comment
names "a provisioning script that enrols servers" as the reason a token holding
`AbilityServers` may call it. Re-ranking the whole route to owner + session-only
would take `public_address` away from every panel admin and every
`servers`/`write` token that sets it today — a break this feature has no
business causing, and one that "`public_address` unchanged" would not survive.
So the check is on the field: a body carrying only `public_address` behaves
exactly as it does now, and a body carrying `agent_channel` is refused unless
the caller is an owner on a session. It is the first field-level rank check in
the API, and it is worth the precedent, because the alternatives are a break or
a channel move available to CI.

All three new routes and the `agent_channel` field are declared in
`core/api/rest/openapi.yaml` first. It is the source of truth for the HTTP
surface (ENGINEERING rule 19) and `web/src/api/gen/` is generated from it, so a
route absent from it does not exist for the panel — not a formality but a step
per route.

`agent.version_set` and `agent.promoted` join the audit vocabulary; the
per-server channel change rides the existing `server.updated`
(`audit.ActionServerUpdated`, already written by that handler). One panel-level
inbox kind joins beside `server.disk_low` — `agent.update_failed`, on the
transition into `rolled_back`, never per heartbeat, which is the rule
[disk-management.md](disk-management.md) §5 states for disk alerting. It is
panel-level for a structural reason recorded in the code rather than in that
spec — `core/domain/inbox.go`, beside `InboxKindServerDiskLow` itself: a Server
belongs to no project, and a Notifier is scoped to one, so channel delivery
waits on panel-level notifiers, which do not exist. (disk-management.md §5 is
stale on this point: it still calls those two kinds "subscribable", which is
not what shipped. The code is the record.) Registering a kind is two edits in
one file and the second is the one that gets missed — the `InboxKind*` const
block **and** the `panelInboxKinds` slice; a kind absent from the slice is
inert. The glossary gains **Release Channel** and **Desired Agent Version**
before any of this copy ships (hard rule 5).

## 8. The screen

`Servers → Updates` — a tab on Servers, not a fifth top-level nav item
(ui-principles §4 fixes the top bar at four, and `web/src/routes/_app.tsx` has
exactly those four: Projects, Servers, Templates, Settings). Routing is
TanStack's file-based tree and `web/src/routeTree.gen.ts` is generated, so
static-over-dynamic precedence is resolved for us and nothing is declared in an
order by hand: adding `web/src/routes/_app/servers/updates.tsx` beside
`index.tsx` and `$serverId.tsx` is the whole change.

The control plane sits on top and says what it does not do:

> **Control plane — cypherd v1.0.3**
> The plane never updates itself. Update deliberately, release notes in hand.
> `Release notes ↗`

Fed by `GET /panel/version`, which already carries the running build and any
newer release's `notes_url`. Guided panel upgrades are a separate feature; the
only thing this screen asserts about the plane is that sentence.

Below it, `AGENTS — DESIRED v1.0.3` with `Promote canary → stable`, over a
table of `SERVER · RUNNING · DESIRED · CHANNEL · STATUS`. Channel is an inline
dropdown per row (`stable ▾` / `canary ▾`). Status renders the phase:
`✓ CONVERGED` when running equals desired; `VERIFYING SIGNATURE…` and the other
in-flight phases in the same voice; `DEGRADED — ROLLED BACK ▸` in amber, where
`▸` expands to which version failed and what it said — a status nobody can
expand is a status nobody can act on; `unknown` for an agent not heard from,
never stale-fresh (ui-principles §§1, 10).

The footer is the security model, and it is the copy:

> Agents fetch signed artifacts themselves over outbound HTTPS and verify
> against a baked-in key — a compromised plane can only pick among genuine
> releases. A failed update re-execs the previous binary and reports degraded:
> stranded at the old version, never off the bus.

## 9. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_AGENT_UPDATE_PRECHECK` | `on` | The plane `HEAD`s the manifest when a channel version is set. `off` for a source only the agents can reach. |
| `CYPHER_UPDATE_DISABLE` | unset | Host-local opt-out for an agent managed by a package manager or baked into an immutable image: it reports `PHASE_DISABLED` and is visibly excluded rather than failing forever, as does one whose binary path is not writable. |
| `CYPHER_UPDATE_PROBATION` | `120s` | How long a new binary has to dial home before it rolls itself back (§4c). |
| `CYPHER_UPDATE_JITTER` | `60s` | Upper bound on the random delay before an agent starts an update. |

## 10. Deliberately out of scope

- **Updating the control plane.** ADR-010 §6; a guided, snapshot-backed panel
  upgrade is a separate feature with its own spec.
- **Percentage or wave-based staged rollout.** ADR-010 §6 names it Later on
  this primitive. Promotion moves every stable host, and the dialog says so.
- **Automatic promotion on a health timer.** §5: the signal is too weak and the
  failure mode is fleet-wide.
- **Maintenance windows for updates.** A freeze window exists to stop new
  application code shipping; scheduling agent updates needs the same zone-aware
  machinery deploy protection built, and deserves its own spec.
- **Per-team or per-project channels.** A Server belongs to no project; the
  fleet is panel-level, exactly as disk alerting is.
- **An "update this agent now" button.** It would be an imperative verb the
  agent obeys — ADR-005 does not allow one and hard rule 3 forbids it. Setting
  the channel is the button; the jitter and the quiescence gate are what make
  "now" mean "as soon as it is safe".
- **Streaming the updater's log to the panel.** The agent's journal is the
  record during the one window when the agent is restarting and can stream
  nothing. The phase and its detail are what cross the wire.
- **Updating Docker, the kernel, or anything else on the host.** The agent
  manages its own binary; this is not a package manager.

## Decisions taken (orchestrator, not the spec author)

**The agent channel moves through its own route, not a field-level rank check on
`PATCH /servers/{id}`.** The spec offered both. A field-level check would be the
first in the API, and a new authorization precedent is a bad thing to introduce
incidentally inside a feature: every check in this codebase is route-shaped, and
a reviewer can see a route's rank at the mux. `PUT /api/v1/servers/{id}/agent-channel`
costs one endpoint and keeps that property.

**The signature check is never stubbed.** ADR-010 §3 forbids it, and
`docs/dev/release-signing.md` records that agent-side verification is
unimplemented while this repository has no published release. The desired-version
model, the channel, the reconciler and the UI are therefore buildable now; the
binary swap is not, and must land with the release pipeline rather than ahead of
it behind a disabled check.
