# Feature spec: Proactive disk management

> A full disk is the failure both reference platforms are best known for, and
> the feature matrix says so plainly: *"Reddit's #1 production killer for both
> tools — silent disk fill until the panel itself crashes."* This closes it in
> the way desired state makes available: **prune anything not referenced**,
> continuously, and say something before the disk is full rather than after.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why the usual fix does not work

The usual fix is a periodic `docker system prune`. Coolify ships one
(`DockerCleanupJob.php`) and still fills disks, for a reason worth stating:
**a prune job cannot tell what is wanted.** It reasons about what is *running*,
so it either spares too much (every stopped container's image, forever) or
takes too much (the image a rollback needs, the moment nothing is using it).
Tuned conservatively it does not reclaim; tuned aggressively it breaks
rollback. There is no setting that is right.

Desired state removes the ambiguity. The plane already knows exactly which
revisions it wants to be able to run — the one deployed, plus the recent ones a
rollback could name. Everything else is, definitionally, not referenced. That
turns garbage collection from a heuristic into a reconciler: **the retain set
is desired state, and the agent converges to it.**

So there is no prune schedule, no aggressiveness dial, and no "clean up now"
button. There is a set of images that should exist, and a reconciler that makes
the host match.

## 2. The retain set

`DesiredState` gains, per application, the revisions whose images must survive:

```proto
message RetainSpec {
  string app_id = 1;
  repeated string revision_ids = 2;
}
```

The plane fills it with the **desired revision plus the most recent
`CYPHERD_REVISION_RETAIN` − 1 others** (default 3 in total), newest first, from
the deployment history it already keeps. The agent removes every managed image
of that application whose revision is not in the set.

Three properties follow, and each one is a bug the prune-job approach has:

- **A rollback target is never reclaimed**, because it is in the set by
  construction rather than by being lucky enough to still be tagged.
- **A stopped application keeps its images**, because it is still desired; only
  a *deleted* one loses them, which is the behaviour that already exists.
- **An empty retain set never means "delete everything".** An application the
  plane could not read revisions for is omitted from `retain` entirely, and an
  omitted application is left alone. Absence means "no instruction" here, not
  "remove" — the opposite of the `specs` list, deliberately, because the cost of
  the two mistakes is not symmetric.

## 3. What else the reconciler reclaims

Alongside the retain set, on every reconcile, the **pull-reference tidy-up**
that already exists — a floating registry reference our own pull created, whose
marker records that it is ours.

And deliberately **not** dangling (`<none>`) images, which is the reclaim
everyone reaches for first. Under the classic builder those are the **build
cache**: removing them frees disk once and makes every subsequent build on that
host slower, permanently. They are not garbage, they only look like it. The
images that actually accumulate — one per revision, forever — are tagged,
attributed and reclaimed precisely by the retain set, which is the same disk
without the cost.

Nothing unmanaged is ever touched. An image an operator pulled by hand, a
container they started themselves, a volume nothing here created: all invisible
to this. The panel cleans up after itself and after nobody else, which is the
only version of this an operator can trust on a shared machine.

**Volumes are never reclaimed by convergence.** That rule already holds for
managed databases and Compose Stacks and it holds here: the failure mode of a
wrong deletion is unrecoverable, and no amount of reclaimed disk is worth it.

## 4. Seeing it coming

The agent reports its Docker data root's filesystem on every heartbeat:

```proto
// In Heartbeat:
uint64 disk_total_bytes = 7;
uint64 disk_free_bytes  = 8;
```

The data root is read from the daemon's own `/info` (`DockerRootDir`) rather
than assumed to be `/var/lib/docker`, because an operator who moved it is
exactly the operator who will not have moved the alert with it. A host where
the figure cannot be read reports zeros, which the plane treats as "unknown"
and never as "full".

The plane stores it on the server and exposes it on the server DTO, so
"how much room is left" is answerable without opening a shell.

## 5. Alerting

Two entries, written to the **notification inbox** for the panel's owners and
admins:

```
server.disk_low        (error)
server.disk_recovered  (info)
```

They are deliberately **panel-level inbox kinds rather than subscribable
events**, and the reason is structural rather than an oversight: a Notifier is
scoped to one project, and a Server belongs to no project, so there is nothing
to resolve a channel against. Registering them in the subscribable taxonomy
would declare a delivery path that cannot fire. `core/domain/inbox.go` is where
that decision lives — `InboxKindServerDiskLow` and `InboxKindServerDiskRecovered`
are in `panelInboxKinds`, and neither appears among the `EventType` values.
Channel delivery for them waits on panel-level notifiers, which do not exist
yet; that gap is named here rather than papered over by attaching a server to
an arbitrary project.

Fired on the **transition** across `CYPHERD_DISK_WARN_PERCENT` (default 85),
never on every heartbeat — a heartbeat arrives every few seconds, and a channel
that repeats itself gets muted, taking the next real alert with it. This is the
same rule, and the same reasoning, that
[deployment-control.md](deployment-control.md) §5 applies to `app.crashed`.

A single threshold, not two. A "warning" and a "critical" level sounds more
careful and is not: the second one only ever arrives after the first has
already been ignored, and it doubles the noise for a single fact. One
threshold, crossed once, with the number in the message.

## 6. The panel's own disk

The control plane reserves nothing and enforces nothing about its own host —
deliberately. `cypherd` refusing to write when its disk is low would turn a
disk problem into an outage of the thing that reports disk problems, at exactly
the moment an operator needs it to work.

What it does instead is tell the truth: `GET /api/v1/panel/version` already
carries the panel's build, and it gains the panel data directory's total and
free bytes, so the one host the fleet view cannot cover is covered.

## 7. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_REVISION_RETAIN` | `3` | Images kept per application, newest first, including the deployed one. Minimum 1 — the deployed revision is never reclaimable. |
| `CYPHERD_DISK_WARN_PERCENT` | `85` | Used-percentage at which a server reports low disk. `0` disables the alert. |

## 8. Deliberately out of scope

- **Reclaiming under pressure.** Behaviour that changes with load is behaviour
  nobody can predict; the retain set is the policy, and it applies the same way
  at 10% full and at 95%.
- **Volume pruning.** §3.
- **Per-server thresholds.** One panel-wide number until someone has a fleet
  heterogeneous enough to need two.
- **Disk usage per application.** A useful thing to see and a different feature:
  it needs the daemon's `/system/df` per resource, and belongs with metrics.
