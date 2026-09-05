# Feature spec: Deployment control

> Four things an operator can do about a deploy or an application once it is
> already moving: stop waiting on one that is going nowhere, restart a
> container without shipping a new revision, ask a log stream for the recent
> past instead of only the live tail, and be told when something crashed —
> and when it came back.
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why these four together

They are the same gap seen from four angles: the panel can *start* things and
then only watch. A deploy stuck behind a builder that will never answer has no
end; a container wedged on a bad connection pool has no cure short of a fake
deploy; a log view that begins at "now" cannot show the thirty seconds that
mattered; and an application that died at 03:00 tells nobody.

Each is small on its own and none is expressible without the others' vocabulary
(a cancel produces a terminal deployment, a restart must not produce one), so
they ship as one spec.

## 2. Cancelling a deploy

`POST /api/v1/deployments/{id}/cancel`.

**A cancel is the panel stopping waiting.** It is not a remote kill: ADR-002
gives the plane no way to reach into a builder and stop a `docker build`, and
inventing one for this would be exactly the imperative "poke the server" path
hard rule 3 forbids. The spec says so plainly, and so does the API description,
because an operator who believes cancel stops a build will be surprised by the
CPU it keeps using for another minute.

What it actually does depends on where the deployment is, and the difference is
the whole design:

| Status | Cancel does | Why |
|---|---|---|
| `queued` | ends it; promotes the next queued deploy | nothing was published; pure bookkeeping |
| `awaiting_approval` | ends it | the requester withdrawing, mirroring an approver's reject |
| `building`, `distributing` | ends it; a later event for it is ignored as late | the work item is out, but desired state has not moved |
| `rolling_out` | **`409`** | desired state HAS moved; see below |
| `succeeded`, `failed` | **`409`** | already finished |

Refusing at `rolling_out` is the load-bearing rule. By then
`applications.desired_revision_id` points at the new revision and every agent
is converging on it. "Cancelling" would leave desired state naming a revision
the panel claims it abandoned — the reconciler would keep converging and the
panel would lie about it. The honest recovery is a rollback, and the `409` says
that in words.

**No new terminal status.** A cancelled deployment ends as `failed` with the
detail `cancelled by <email>`, for the reason
[deploy-protection.md](deploy-protection.md) already recorded when it declined
to add one for rejection: the terminal set (`succeeded`, `failed`) is
load-bearing in two queue queries, in `DeploymentStatus.Terminal()` and in the
web's `isTerminal()`, and a sixth status would touch all of them for no
observable gain. A cancelled deploy is a deploy that did not ship.

Cancelling is `deploy`-ability, like deploying and rolling back — the same
credential that can start one can stop one. Team `member` rank, matching
deploy.

**Late events.** A build that completes after its deployment was cancelled
arrives at a terminal deployment. The scheduler already ignores those, and the
cancel path adds no new case: the image the builder produced references no
revision anything desires, so desired-state GC reclaims it on the next
reconcile.

## 3. Restarting an application

`POST /api/v1/applications/{id}/restart`.

The naive version of this is an imperative verb the agent obeys. ADR-005 does
not allow one, and it would be the worst kind of exception: a reconciler that
takes orders on the side stops being able to converge from desired state alone.

So a restart is **desired state**, expressed as a token:

```
applications.restart_token TEXT NOT NULL DEFAULT ''   -- 0037
AppSpec.restart_token = 17                            -- work.proto
```

The token is part of the container's config hash. The reconciler already
recreates a container whose config hash does not match its spec — that is how
an env-var change takes effect — so a new token *is* a difference in desired
state, and the existing recreate path handles it with no new branch. Converging
twice still mutates nothing: after the recreate, the running container carries
the token the spec names.

Restarting is **not a deploy**. No Revision is created, no build runs, no
Deployment row appears, and `desired_revision_id` does not move — an operator
restarting a wedged container must not silently ship the config they edited an
hour ago. The response is the application, not a deployment.

It carries the same rank as a deploy (`member`, `deploy` ability): it is a
production action with a visible effect. It is audited as
`application.restarted`. Deploy protection does **not** gate it, and that is
deliberate: a freeze window exists to stop new code shipping, and a restart
ships none. Refusing to restart a crashed application during a freeze would
make the freeze an outage.

Restarting an application that has never deployed is a `409` — there is no
container to restart, and the honest answer is "deploy it first".

## 4. `since` on log streams

`GET /api/v1/applications/{id}/logs?since=…` and the same on
`GET /api/v1/deployments/{id}/logs`.

Both streams are ordered JetStream consumers. Runtime logs already retain a
bounded window ([bounded-log-retention.md](bounded-log-retention.md)); the
stream simply starts at `DeliverAll`, so a client sees everything retained and
then the live tail. `since` moves that start.

Accepted forms, in the order they are tried:

- a Go duration — `since=15m`, `since=2h` — meaning "that long ago";
- an RFC 3339 instant — `since=2026-09-06T09:00:00Z`.

Anything else is a `400` naming both forms, rather than a silent fallback to
the whole window: a client that meant "the last minute" and got four hours has
been given the wrong answer confidently.

A `since` in the future is not an error — it is an empty stream that then
tails, which is what it literally asks for. A `since` older than the retention
window yields whatever is retained; there is no way to promise more, and
pretending otherwise would need a second store.

## 5. Crash and recovery events

An application transitioning to `error` — and back — becomes two new entries in
the **subscribable** event taxonomy:

```
app.crashed    (error)
app.recovered  (info)
```

Subscribable, not inbox-only. The governance kinds (`deploy.awaiting_approval`,
`access.requested`) are inbox-only because they are news for named people about
a decision. A crash is not: it is an observed terminal transition of a resource,
which is exactly what `domain.eventTypes` is for — and "tell me in Discord when
production dies" is the single most-wanted notification a PaaS has. Adding the
keys to `eventTypes` gives notifiers, outbound webhooks and the inbox all three
at once, which is what that one-place taxonomy exists to make true.

**Transitions, not states.** The event fires when the *stored* status changes,
never on every observation. An agent reports status continuously; firing per
observation would deliver a Discord message every few seconds for one dead
container, and an operator who mutes that channel has lost the next real crash
too.

Exactly two transitions fire:

- `running` → `error` fires `app.crashed`;
- `error` → `running` fires `app.recovered`.

Everything else is silent, and the two exclusions are the point:

- **`deploying` → `error` fires nothing.** A rollout whose health gate fails
  reports `error` while the *old* container is still serving — nothing crashed,
  a deploy did not land, and `deploy.failed` already says so. Requiring the
  previous status to be `running` excludes that whole class without needing to
  ask whether a deployment is in flight.
- **`degraded` fires nothing.** It means "serving, with something wrong", and a
  channel that pages on it teaches operators to ignore the channel.
- `error` → `error` fires nothing, whatever the detail says, and a transition
  to `stopped` fires nothing: that is a person's decision, not a failure.

The event carries the application, the observed detail (the container's own
last words, which is the whole diagnostic value) and the environment, so the
inbox can deep-link it exactly as a deploy outcome does.

## 6. API

| Route | Ability | Rank | Notes |
|---|---|---|---|
| `POST /api/v1/deployments/{id}/cancel` | `deploy` | member | `409` while rolling out or once finished |
| `POST /api/v1/applications/{id}/restart` | `deploy` | member | `409` before the first deploy; not gated by a freeze |
| `GET /api/v1/applications/{id}/logs?since=` | `read` | member | duration or RFC 3339 |
| `GET /api/v1/deployments/{id}/logs?since=` | `read` | member | same |

## 7. Deliberately out of scope

- **Stopping and starting an application.** A stop is a desired-state change
  (`replicas: 0`, or a `stopped` desire), it interacts with routing, health and
  the reconciler's absence-means-remove contract, and it deserves its own spec
  rather than a paragraph in this one.
- **Killing a running build on the builder.** Needs an agent-side verb; see §2.
- **Downloading a log window as a file.** The SSE stream is the API; a
  file export is a UI affordance over it.
- **Alerting policy** (thresholds, repeat suppression, escalation). `app.crashed`
  is an event, not an alerting system; the muting that exists is the inbox
  preference list.
