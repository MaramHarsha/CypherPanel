# ADR-011: In-container scheduled-task commands as declarative desired state

- **Status:** Proposed
- **Date:** 2026-07-20

## Context

Scheduled tasks (cron-in-container) are a **V1** feature
([feature-matrix.md](../product/feature-matrix.md) row "Scheduled tasks";
[roadmap.md](../roadmap.md) Phase 3): an operator declares "run this command
inside my app on this schedule" — database migrations at 2am, cache warmers,
report generators, cleanup jobs. Both references ship it
([research/coolify.md](../../research/coolify.md) `ScheduledTaskJob`,
[research/dokploy.md](../../research/dokploy.md) `apps/schedules`).

This collides with a **Phase 1 security invariant** we have guarded carefully:

- **Threat-model §8 req 4:** *"The wire protocol contains no arbitrary-command
  verb… work.*/agent messages describe state, never shell strings."*
- **Threat-model §5.1 (the defining threat):** *"no component accepts 'run
  this arbitrary command' over the wire… the plane must never be able to hand
  the agent an arbitrary shell command."*

The invariant is airtight today: `AppSpec` (work.proto) carries no command
field at all — the agent only ever runs an image's built-in entrypoint. The
one shell string on the wire, `DbSpec.health_cmd`, is a declarative Docker
`HEALTHCHECK` directive (container config Docker itself runs), not a verb the
agent execs. The database-backup feature went out of its way to honor req 4:
rather than send a dump command, it sends the **engine name** and the agent
derives the command from its own matrix (ADR-adjacent, managed-databases.md
§7).

A scheduled task has **no matrix to derive from** — the command is arbitrary
operator input and is the entire point of the feature. It must reach the
agent. So the invariant, as literally written ("never shell strings"), forbids
the feature. This is a genuine architectural decision, not an implementation
detail (CLAUDE.md rule 2: changed circumstances → a superseding/refining ADR),
and it is why this ADR exists before any code.

## Decision

**A scheduled-task command is permitted on the wire as a declarative property
of an Application's desired state — never as an imperative "run now" verb — and
executes only inside that Application's own unprivileged container.** Req 4 is
**refined** (not discarded): the boundary is not "no shell strings ever" but
**"no verb that can execute outside a workload's own sandbox."**

Concretely:

1. **It is desired state, not a command verb.** A `ScheduledTask {id,
   schedule, command}` list rides on `AppSpec` — the same desired-state message
   that already declares the app's image, env, health probe, and route. The
   plane **declares** tasks; it never publishes a "execute this now" work item.
   The agent's reconciler owns cron evaluation and runs a task when it is due,
   exactly as it already runs the health probe on a timer. There is no
   run-trigger RPC to compromise.

2. **Execution is confined to the app's own container.** The agent `exec`s the
   command inside the *already-running application container* — same image,
   same unprivileged user, same cgroup limits, same network namespace, same
   Docker sandbox the operator's own code already runs in. **Never** the host,
   **never** a privileged container, **never** the Docker socket, **never** a
   new capability.

3. **Argv, not a shell.** `command` is a string list passed straight to
   `exec` (no implicit `sh -c`). An operator who wants shell features writes
   `["sh","-c","…"]` explicitly (as in Kubernetes) — so the agent never
   parses a shell string, and the wire never hides one.

4. **Observed, bounded, auditable.** Each run reports a `ScheduledTaskRun`
   observation (started/finished, exit code, bounded output tail) on a
   `state.*` subject — the plane records history but asserts nothing from
   work-item completion (ADR-005). Runs are per-app, per-identity, and the
   output tail is capped (§5.9 retention discipline).

### Why this preserves the §5.1 property

The property that must hold is: *compromising the control plane must not grant
a raw shell on a server; the blast radius is bounded to what the desired-state
model can express.* A compromised plane that sets a scheduled-task command `X`
in container `C` is **exactly as powerful as** a compromised plane that
deploys an image whose entrypoint is `X` — both run attacker-chosen code
**inside the app's own unprivileged container**, and the latter is already the
accepted, documented residual risk of §5.1 ("can deploy malicious images…
what they cannot do is get a root shell fleet-wide"). Scheduled tasks add **no
privilege** the deploy path did not already grant. What §5.1 forbids — a raw
host shell, execution outside the sandbox, a general remote-exec verb spanning
arbitrary containers or the host — this design still forbids, because the exec
target is pinned to the app's own container and there is no general exec verb,
only per-Application desired state.

The distinction is the whole decision: **a general "exec arbitrary command"
verb (SSH-style) is still banned; a workload declaring its own in-sandbox
maintenance tasks is desired state.**

## Alternatives considered

- **Plane-side cron + a `RunScheduledTaskWork` verb.** The plane evaluates
  schedules and dispatches a "run this command now" work item. Rejected: this
  is precisely the imperative run-trigger verb §5.1 warns against — it puts a
  command on the wire as a *control action*, not as state, weakening the
  security story for no benefit. Agent-side evaluation of declared state is
  strictly purer (ADR-005) and needs no run verb.

- **Derive commands from a matrix (the backup pattern).** Impossible: operator
  maintenance commands are open-ended; there is nothing to derive.

- **Bake tasks into the image / a sidecar the operator builds.** Pushes the
  feature onto the user, defeating it; and a sidecar cron still needs the
  command, relocating the same question without answering it.

- **Refuse the feature at v1.** Rejected: it is a V1 feature-matrix commitment
  and a common, low-controversy PaaS capability; the security concern is
  answerable precisely (above) rather than by omission.

## Consequences

- **Proto (additive, buf-breaking-clean):** `AppSpec` gains
  `repeated ScheduledTask scheduled_tasks`; a new `ScheduledTaskRun` state
  message and a `state.*` subject for run observations. Field numbers are only
  added, never reused (ENGINEERING 12/14/18).
- **Agent gains a cron reconciler:** it evaluates each task's schedule against
  the host clock and `exec`s due tasks in the app container via the existing
  `ExecAndWait` surface (reused from backups). Overlap policy: a task still
  running when its next tick arrives is **skipped** (logged), not stacked.
- **Cron parsing:** standard 5-field cron. The feature spec picks between a
  small vetted dependency (`robfig/cron/v3`, parser only) and a bounded
  internal parser; correctness of schedule matching is the deciding factor and
  is settled in the spec, not here.
- **A `state.*` run subject and run-history rows** follow the §5.9 bounded-
  retention rule (capped output tail, capped run history per task).
- **Threat-model update:** §8 req 4's wording is refined from "never shell
  strings" to the sandbox-boundary rule above, with a back-reference to this
  ADR, so the invariant stays honestly documented rather than silently bent
  (this edit lands with the feature per CLAUDE.md rule 7 / ENGINEERING doc-with-
  behavior).
- **Not in scope / still forbidden:** any general exec verb, host-level or
  privileged execution, exec into containers other than the task's own
  Application, and an imperative "run now" control RPC (a manual run, if added
  later, must itself be expressed as desired state the agent observes).
- **Reversibility:** the schema and proto additions are additive; disabling the
  feature is removing the tasks from desired state (the agent converges to no
  cron). No data-destructive migration.
