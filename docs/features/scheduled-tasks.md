# Feature spec: Scheduled tasks (cron-in-container)

> An operator declares "run `php artisan schedule:run` inside my app every
> minute", or "`rails db:cleanup` at 3am". The panel runs it on schedule,
> inside the app's own container, and records each run's outcome. No external
> cron box, no SSH, no manual trigger. This is a **V1** feature
> ([feature-matrix.md](../product/feature-matrix.md) row "Scheduled tasks")
> and the last data-plane item in the Phase 3 bucket
> ([roadmap.md](../roadmap.md)).
>
> Written 2026-07-20, just before implementing. Vocabulary per
> [glossary.md](../glossary.md). Security posture set by
> [ADR-011](../adrs/ADR-011-in-container-scheduled-tasks.md) — read it first.
> Cron-in-container semantics ported from
> [research/coolify.md](../../research/coolify.md) (`ScheduledTaskJob`) and
> [research/dokploy.md](../../research/dokploy.md) (`apps/schedules`) — logic
> only, never code (CLAUDE.md rule 1).

## 1. The core idea: a task is desired state, the agent owns the clock

Per [ADR-011](../adrs/ADR-011-in-container-scheduled-tasks.md), a scheduled
task is **not** an imperative "run this now" verb (that would breach
threat-model §5.1). It is a **declarative property of an Application's desired
state**: the app *declares* "these commands run on these schedules", carried on
the same `AppSpec` message that already declares the image, env, health probe,
and route. The **agent's reconciler owns cron evaluation** and runs a task when
it is due — exactly as it already runs the health probe on a timer — executing
**inside the app's own unprivileged container** (never the host, never
privileged, never the Docker socket). There is no run-trigger RPC to compromise.

```
operator ──create task──▶ plane (desired state)
                             │  task rides on AppSpec.scheduled_tasks
                             ▼
                          agent w.state[app]  ──cron reconciler ticks──▶ exec in app container
                                                                              │
                                    ScheduledTaskRun observation ◀────────────┘
                             plane records run history (ADR-005: asserts nothing
                             from work-item completion, only from observations)
```

## 2. The resource model

A **ScheduledTask** is attached to an Application (the workload it runs inside):

```
ScheduledTask:
  id             TEXT PK (sch_ prefix)
  application_id TEXT NOT NULL → applications(id) ON DELETE CASCADE
  name           TEXT NOT NULL
  schedule       TEXT NOT NULL       -- standard 5-field cron ("0 3 * * *")
  command        TEXT[] NOT NULL     -- argv, passed straight to exec (no shell)
  enabled        BOOLEAN NOT NULL DEFAULT true
  created_at, updated_at
  UNIQUE(application_id, name)
```

A **ScheduledTaskRun** records one execution (the observation the agent
reports), retained bounded per task (§6):

```
ScheduledTaskRun:
  id           TEXT PK (str_ prefix)
  task_id      TEXT NOT NULL → scheduled_tasks(id) ON DELETE CASCADE
  started_at   TIMESTAMPTZ NOT NULL
  finished_at  TIMESTAMPTZ
  exit_code    INTEGER
  status       TEXT NOT NULL          -- running | succeeded | failed
  output_tail  TEXT NOT NULL DEFAULT '' -- last N KB of stdout+stderr, diagnostic
```

**`command` is argv, not a shell string** (ADR-011): the list is passed
straight to `exec`. An operator who needs shell features writes
`["sh","-c","…"]` explicitly (as in Kubernetes) — so the agent never parses a
shell string and the wire never hides one.

## 3. Desired-state wiring (proto, additive)

`AppSpec` gains a repeated field (field numbers only ever added — ENGINEERING
12/14/18, buf-breaking-clean):

```proto
message AppSpec {
  // … existing fields 1–9 …
  repeated ScheduledTask scheduled_tasks = 10;
}
message ScheduledTask {
  string id = 1;
  string schedule = 2;         // 5-field cron
  repeated string command = 3; // argv (ADR-011: never a shell string)
}
```

A new observation message + subject (mirrors `DbStatus` on `state.*`):

```proto
message ScheduledTaskRun {
  string task_id = 1;
  string run_id = 2;          // plane-minted idempotency key for the run row
  google.protobuf.Timestamp started_at = 3;
  google.protobuf.Timestamp finished_at = 4;
  int32 exit_code = 5;
  bool failed = 6;            // exec error or non-zero exit
  string output_tail = 7;     // capped; diagnostic
}
```

Subject: `state.<server_id>.task` (per-server, like `state.<server>.dbbackup`).
The agent mints `run_id` locally so a redelivered observation is idempotent on
the plane.

The plane populates `scheduled_tasks` from the **store** (the app's current
*enabled* tasks), not from the revision snapshot — tasks are mutable
independently of revisions, exactly like env vars are applied from current
state at spec-build time. Three code paths carry them:

- `buildSpec` (rollout on deploy),
- `DesiredStateFor` (full sync on agent (re)connect),
- **converge push** on task CRUD (§4).

## 4. Propagation: converge without a redeploy

Adding or editing a task must take effect without recreating the container.
On any task CRUD, the plane rebuilds the app's `AppSpec` at its **current
desired revision** and publishes a **`ConvergeWork { AppSpec spec }`** on
`work.<server_id>.<app>.converge`. The agent handler sets `w.state[app]` and
runs a silent reconcile (`STAGE_UNSPECIFIED` — **no** deploy event emitted):

- The **container reconcile is a no-op** — image and container config are
  unchanged, so nothing is recreated (the reconciler is idempotent, rule 13;
  `scheduled_tasks` is deliberately *not* part of container identity).
- The **cron reconciler re-arms** its task set from the new spec.

If the app has no desired revision yet (never deployed), converge is a no-op —
there is no container to run tasks in; the tasks apply on first deploy. This
reuses the existing desired-state machinery rather than inventing a run verb
(ADR-011).

## 5. The agent cron reconciler

A small reconciler beside the app driver (a builder-role agent, which has no
app driver, skips it):

- Holds an **armed set** derived from `w.state[*].scheduled_tasks`: for each
  task, the parsed schedule and its next-fire time. Re-derived on every
  converge/sync so create/edit/delete/enable changes are picked up; a removed
  task is dropped, a changed schedule re-armed.
- A **30s ticker** (finer than cron's 1-minute resolution, so no minute is
  missed) fires due tasks. For each due, enabled task **not already running**
  (overlap policy: **skip**, logged — never stack a slow task on itself):
  1. Resolve the app's **currently-running container** by label
     (`cypherpanel.app-id`); if none is running, skip (nothing to exec into).
  2. `ExecAndWait(containerID, command)` — the exec surface already used by
     backups; runs as the container's normal user, bounded by its limits.
  3. Capture exit code + a **capped output tail** (last 4 KB of stdout+stderr).
  4. Publish one terminal `ScheduledTaskRun` observation.
  5. Compute the next fire time (`schedule.Next(now)`).

Next-fire times live **in agent memory**: on restart the agent re-arms from
"now", so a run missed while the agent (hence the container) was down is
skipped — consistent with "agent down ⇒ workload down ⇒ no runs", not a
backlog to replay.

**Cron parsing:** standard 5-field cron via `github.com/robfig/cron/v3`
(parser only — `cron.ParseStandard(...).Next(t)`). Rationale: schedule-matching
correctness (ranges, steps, lists, the day-of-month/day-of-week OR rule) is
fiddly and a subtle bug runs tasks at the wrong time; a mature, pure-Go,
zero-transitive-dependency library is the lazy *and* correct choice over a
hand-rolled calendar parser (ponytail). The plane validates the same expression
at create time using the same parser, so a bad cron is a 400, never a silent
no-run.

## 6. Security & bounds

- **ADR-011 boundary:** exec is confined to the app's own unprivileged
  container (resolved by `cypherpanel.app-id` label), argv not shell, no
  general exec verb, no host/privileged/socket access. Blast radius equals the
  already-accepted deploy residual (§5.1) — no new privilege.
- **Output is diagnostic and capped** at 4 KB tail per run (§5.9 retention
  discipline). It is the task's own stdout/stderr; it may contain whatever the
  task prints, so it is treated like build logs — bounded, and the run-history
  API is behind operator auth.
- **Run history is bounded**: the plane keeps the most recent N runs per task
  (default 20) and prunes older rows on each new run (same pattern as backup
  record retention).
- **Validation:** cron expression must parse; `command` must be non-empty;
  `name` required and unique per app.

## 7. API surface (under `/api/v1`)

```
POST   /applications/{id}/scheduled-tasks   → 201 ScheduledTask
GET    /applications/{id}/scheduled-tasks   → [ScheduledTask]
GET    /scheduled-tasks/{id}                 → ScheduledTask
PATCH  /scheduled-tasks/{id}                 → ScheduledTask   (name, schedule, command, enabled)
DELETE /scheduled-tasks/{id}                 → 204
GET    /scheduled-tasks/{id}/runs             → [ScheduledTaskRun]  (recent history)
```

Create/patch validate the cron expression and command server-side. A manual
"run now" is **out of scope** this slice (ADR-011: if added, it must itself be
expressed as desired state the agent observes, not an imperative RPC).

## 8. Acceptance (testable)

1. Create an enabled task `* * * * *` running `["sh","-c","echo hi"]` on a
   deployed app → within ~a minute a `ScheduledTaskRun` appears with exit code
   0 and `status=succeeded`; `GET …/runs` lists it.
2. A task whose command exits non-zero → a run with `status=failed` and the
   captured output tail.
3. Edit the schedule / disable the task via PATCH → the change takes effect
   without a redeploy (converge push); a disabled task stops running.
4. Delete the task (or the app) → the task, its runs, and the agent's armed
   entry are all gone; the container is untouched.
5. A task that runs longer than its interval does not stack — the overlapping
   tick is skipped, not queued.
6. An invalid cron expression on create → 400, no task stored.

## 9. Out of scope this slice

Manual "run now" trigger · per-run streaming/live logs (only a terminal output
tail) · timezone selection (host/container clock, UTC) · one-off (at) schedules
· running a task in a *fresh* container rather than the running one · catch-up
/ backfill of runs missed while the agent was down · task-level env overrides
(the task inherits the app container's environment) · concurrency policies
beyond skip-if-running.
