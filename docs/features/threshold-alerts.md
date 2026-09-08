# Feature spec: Metric threshold alerts

> [metrics-and-usage.md](metrics-and-usage.md) §14 deferred this by name —
> *"this ships the numbers, not the policy"* — and
> [app-scaling.md](app-scaling.md) §13 deferred it again from the other side
> (*"alerting policy — thresholds, repeat suppression, escalation"*). This is
> that policy. A rule reads as one sentence — **when `memory` on
> `srv-frankfurt-1` stays above `90%` for `5 min`, notify `team-alerts`** — and
> before it is saved the panel says how often it *would* have fired over the
> last seven days. That last part is the feature: an alerting system almost
> never fails by missing a page, it fails by paging nightly until somebody mutes
> the channel and loses the next real one.
>
> Written 2026-09-06, just before implementing (CLAUDE.md rule 7). Vocabulary
> per [glossary.md](../glossary.md), which gains **Alert Rule**, **Signal**,
> **Firing** and **Backtest** in the same PR. **Depends on
> [metrics-and-usage.md](metrics-and-usage.md)**, which owes this feature one
> row it does not yet emit (§3.2).

## 1. What this is, and the failure it is designed against

The panel can already say *something is wrong*: `app.crashed` when a container
dies ([deployment-control.md](deployment-control.md) §5), `server.disk_low` when
a host crosses one panel-wide percentage
([disk-management.md](disk-management.md) §5). Both are boolean facts. Neither
can say *this is getting worse* — memory at 94% for two hours, p95 tripled since
Tuesday's deploy. Those are questions about a **magnitude over a window**, and
until metrics-and-usage there was nothing to ask them of.

Now there is, and the temptation is the obvious thing: a threshold, a
comparison, a message. The obvious thing is what every operator has already
muted somewhere. Four ways it goes wrong, one section each: it fires on a single
sample (§4); it never says it stopped, so the channel becomes a list of things
that may or may not still be true (§5); it flaps (§5); and the operator has no
idea what number to type, guesses, gets paged at 03:00 for a week, and deletes
the rule — so a badly chosen threshold produces not a noisy alert but **no
alert** (§6).

**On ADR-005.** An Alert Rule is a declarative row and that row is its entire
desired state. Delivery is a **plane-internal reaction** to numbers agents
already reported, exactly as [notifications.md](notifications.md) §1 frames a
Notifier: no agent involved, no server asked to do anything, no new subject,
nothing crossing the agent↔plane boundary. And **nothing here acts** — an alert
delivers a sentence. The version that does something about high CPU is the
autoscale rule ([app-scaling.md](app-scaling.md) §9), which writes desired state
and needs a deadband because its mistakes cost an outage. Keeping them apart is
what lets this one be as sensitive as an operator likes.

## 2. The sentence is the data model

```
alert_rules
  id              TEXT PK (alr_ prefix)
  target_kind     TEXT NOT NULL         -- server | application
  target_id       TEXT NOT NULL         -- srv_… / app_… — deliberately not an FK
  signal          TEXT NOT NULL         -- cpu | memory | disk | p95_latency_ms
                                        -- | requests_per_second
  threshold       NUMERIC NOT NULL
  threshold_unit  TEXT NOT NULL         -- percent | bytes | milliseconds | per_second
  window_seconds  INTEGER NOT NULL      -- the "for", a multiple of the metrics bucket
  notifier_id     TEXT NOT NULL REFERENCES notifiers(id) ON DELETE RESTRICT
  enabled         BOOLEAN NOT NULL DEFAULT true
  state           TEXT NOT NULL DEFAULT 'no_data'  -- ok | firing | no_data | flapping
  state_since     TIMESTAMPTZ NOT NULL
  rearm_until     TIMESTAMPTZ           -- NULL unless the flap guard holds it (§5)
  created_at, updated_at
  UNIQUE (target_kind, target_id, signal, threshold, window_seconds, notifier_id)

alert_events
  id            TEXT PK (ale_ prefix)
  rule_id       TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE
  started_at    TIMESTAMPTZ NOT NULL
  resolved_at   TIMESTAMPTZ            -- NULL while firing
  peak_value    NUMERIC NOT NULL       -- the worst bucket in the episode
  delivered     BOOLEAN NOT NULL DEFAULT true   -- false when the flap guard held it
  INDEX (rule_id, started_at DESC)
```

**No `name` column.** The sentence names the rule, rendered from the row, so the
list, the modal, the Discord message and the API say the same words and no label
typed in March drifts from what the rule now does — the argument
[outbound-webhooks.md](outbound-webhooks.md) §2 made against naming a Webhook
Endpoint. The unique constraint does that spec's second job too: it stops the
double-add that would silently deliver every alert twice. Two rules differing
only in notifier are legal and useful (page on-call, post to a status channel),
which is why `notifier_id` sits inside the constraint.

**`target_id` is not a foreign key**, for metrics-and-usage §6's first reason and
not its second: `target_kind` is polymorphic, so there is no column to point at.
The *second* reason — a cascade would rewrite history — does not apply, and the
difference decides the cleanup. An audit event is *evidence* and must outlive
what it names; an alert rule is *configuration*, and configuration for a deleted
application is noise. So the deletion path drops its rules in the same
transaction, and the audit log keeps the record that it happened.

**`notifier_id` is `ON DELETE RESTRICT`**, so a notifier cannot vanish and leave
a rule silently undeliverable — a smoke detector whose battery someone removed in
another room. The 409 **names the rules**, as [registries.md](registries.md) does
with `used-by`.

**`threshold_unit` is stored, not derived**: `90` is percent for a server's
memory and bytes for an application's disk, and a rule must never be
reinterpreted in a unit it was not written in because its target's config changed
later. Same discipline as metrics-and-usage's `histogram_version` — the row says
what its own numbers mean.

**State lives on the rule** (one rule, one target, no second dimension to key a
table by), and all four states are visible in the API and the list, including the
two that deliver nothing: a rule quiet for a bad reason must not look like one
quiet for a good reason (ui-principles §10). `alert_events` holds one row per
**episode**, not per evaluation, `CASCADE` because it is the rule's own history
rather than a record of what a principal did. It is what "fired 3× this week" is
counted from, and what makes the backtest checkable (§13.4).

## 3. The signals, and what each number actually is

### 3.1 The matrix

Five signals. Four of them are the four [app-scaling.md](app-scaling.md) §9.1
names for the autoscale rule — `cpu`, `memory`, `requests_per_second`,
`p95_latency_ms` — deliberately the same words, because two screens offering
`p95 latency` and `p95_latency_ms` for one number is a vocabulary bug. The fifth
is `disk`, which that rule has no use for (no replica count fixes a full volume)
and which is the signal an operator asks about most.

| Signal | Application | Server | Read from |
|---|---|---|---|
| `cpu` | ✅ % of one core | ✅ % of the host's cores | `cpu_core_ms / covered_seconds` |
| `memory` | ✅ % of its limit, or bytes without one | ✅ % of host RAM | `memory_byte_seconds`, `memory_limit_bytes` |
| `disk` | ✅ bytes, hourly | ✅ % of the Docker data root | `resource_disk_usage` / the host row |
| `p95_latency_ms` | ✅ routed only | ✗ | `request_metrics.latency_buckets` |
| `requests_per_second` | ✅ routed only | ✗ | `requests / covered_seconds` |

**`p95` and `requests/s` are refused on a server**, and the refusal is
substantive. A server does have request buckets — the unmatched-router rows
metrics-and-usage §4.3 keeps so "traffic is arriving here and hitting nothing"
stays answerable. That is scanners and wrong Host headers. `p95 on
srv-frankfurt-1` would silently mean "p95 of the requests that matched nothing",
and a signal whose meaning needs a footnote is the wrong signal.

**`cpu` on an application is percent of one core, uncapped** — metrics-and-usage
§1 opens with a container "pinned at 190% of a core", a legal threshold here;
normalising against host cores would make a pegged application on a 32-core box
read 3%. **`cpu` on a server is percent of the host's cores**, the number a
person means by "the box is at 90%". Same signal, two denominators, and the modal
names which one inline — the "explain it cold" test (ui-principles §11) demands
that line anyway. **`memory` on an application** is a percentage when it has a
`memory_limit_mb` and bytes when it does not, because otherwise there is no
honest denominator; the unit is fixed at create time and adding a limit later
never silently converts an existing rule.

### 3.2 What metrics-and-usage owes this feature: one host row

This is the part that does not work today, and it is the design screen's own
headline example.

metrics-and-usage attributes every sample to a **managed** container — *"a
container carrying none of those is not ours and is not sampled"* (§4.1) — and
`GET /servers/{id}/metrics` is those containers summed. So "memory on
srv-frankfurt-1 above 90%" computed from that data is the share of host RAM taken
by CypherPanel's own containers, and it is wrong in the dangerous direction: on a
box the operator's own workload filled, it reports calm.

The fix belongs in that spec's collector, not in a second collection path here:

> **The agent's sampler emits one `resource_kind = 'server'` row per bucket per
> host**, carrying the host's own CPU, memory and data-root filesystem — from
> `/proc/stat`, `/proc/meminfo` and the same `statfs` the heartbeat already
> performs (`agent/heartbeat/heartbeat.go`). Not a sum of containers: the host's.

It costs **288 rows per server per day**. metrics-and-usage §2's ≈12 600 is the
whole-install figure for its representative **3 servers**, so the honest
comparison is 3 × 288 = **864 rows/day, about 7%** — a real number rather than a
rounding error, and small enough that that spec's budget claim stays checkable
with it added. It needs three additive columns on `resource_metrics` —
`cpu_limit_millicores` (the host's cores × 1000, so a percentage from a
two-week-old bucket uses the core count that bucket was measured against rather
than today's after a resize), `disk_total_bytes` and `disk_used_bytes`;
`memory_limit_bytes` already exists and carries host RAM on a server row.

**Server disk is already observed; what is missing is history.**
`0039_server_disk.sql` stores `servers.disk_total_bytes`, `disk_free_bytes` and
`disk_low`, and the agent reports the daemon's own `DockerRootDir` filesystem by
`statfs` on every heartbeat (`agent/heartbeat/heartbeat.go`) — that figure is
what `server.disk_low` fires on and what the fleet view already shows. But a
heartbeat is the current number and nothing else: it cannot express *"for
5 minutes"* and there is nothing to backtest against (§12). Putting the same
measurement on the 5-minute grain is the whole of what this row adds for disk.

It deliberately breaks metrics-and-usage §4.1's *"the panel measures what it
manages and nothing else"* for this one row, and the exception is principled: a
**Server** *is* something the panel manages. Per-resource attribution stays
managed-only, because "what is this application responsible for" is a question
about our workloads; "is this box out of memory" is a question about the box.
`/proc` is Linux, which is every host the installer supports (`install/agent.sh`
refuses a non-Linux host outright); a host that cannot read it reports nothing
and its rules sit in `no_data` with that reason, never at zero — ui-principles §5
(`unknown` *"never fake certainty"*) and §10, the same rule that makes a
`disk_total_bytes` of zero read as unknown rather than full.

### 3.3 Grain, and refusals at create time

`window_seconds` must be a whole multiple of `MetricsSettings.bucket_seconds`
(default 300) — the evaluator counts buckets and half a bucket is not readable —
so the picker offers **5 min · 10 min · 15 min · 30 min · 1 h · 2 h · 6 h**, a
select rather than a free-text field, which is why the design draws it `5 min ▾`.
An application's `disk` is **hourly** (metrics-and-usage §4.2 makes the daemon's
verbose disk call hourly on purpose) and so offers `1 h` upward: not worth
engineering around, since disks fill over days and an alert an hour late about a
growing volume still carries days of warning. If an operator raises
`bucket_seconds` — that spec expects a 500-container install to move it to 900 —
existing rules whose window is no longer a multiple go to `no_data` with the
reason named and a one-click fix, never silently re-rounded.

A rule is validated against the target it names: `p95`/`requests_per_second` on an
un-routed application or on a server, a memory percentage on an application with
no limit, a bad window, or a notifier out of scope (§7) are each a `400` naming
the reason. This is [registries.md](registries.md)'s rule — **a capability is
checked when it is attached, not when it is spent** — and it is here for that
spec's reason: a rule that can never fire is not waiting, it is broken, and the
operator who wrote it believes they are covered.

## 4. Never on one sample: the sustained window

The condition is not "the value is above the threshold". It is: **every bucket
covering the last `window_seconds` breached, and there are no gaps in that span.**

**Whole buckets, not instants.** One bucket is already an aggregate over roughly
twenty samples, so a four-second spike moves a 5-minute mean by about 1% and does
not fire. The minimum window is one bucket rather than "one sample" because
sample-level data does not exist on the plane by design (metrics-and-usage §2) —
never having stored it is what makes even the cheapest rule immune to the single
spike. The compared value is the bucket's **mean**, from the accumulator, not
`cpu_percent_peak`, which is a single sample by definition and would reintroduce
exactly what the window removes. The peak is not discarded: it is the
notification's `peak_value` ("peak 96% at 14:35"), the number an operator wants
once they know the alert is real.

**Coverage is a precondition, not a value.** A bucket whose `covered_seconds` is
under half its nominal length — an agent restart, a proxy recreate — is
**`unknown`**, neither high nor low. metrics-and-usage §3 stores coverage so a
partial bucket is never read as a dip; here it is never read as a breach either.
Dividing a spike by 40 seconds of coverage would manufacture a convincing 300%
CPU reading out of an agent restart.

**A gap breaks the run and resolves nothing.** Any missing or `unknown` bucket in
the window means the rule does not fire — and does not resolve. It goes to
`no_data` until the span is whole. Treating missing as below-threshold is how an
alerting system reports all-clear during an outage of its own collection path,
which is the worst thing it can say. `no_data` is a state, not silence: a rule in
it for **24 hours** writes one notification-inbox item, once on that transition,
to the audience its events would reach. "Your alert has not been watching
anything since Tuesday" is news for a person and does not belong on an ops
channel — the line [notification-inbox.md](notification-inbox.md) draws.

That item costs more than a string, and the spec says so rather than assuming a
generic writer exists. The inbox taxonomy is a **closed set** validated by
`domain.ValidInboxKind` over `panelInboxKinds` / `protectionInboxKinds` / the
team kinds (`core/domain/inbox.go`), and every inbox-only kind carries its own
method on `core/inbox.Service` (`RecordPanelUpdate`, `RecordServerDisk`,
`RecordDeployAwaitingApproval`, …) — there is no generic path to write down. So
this feature adds **two inbox-only kinds**, `alert.no_data` here and
`alert.flapping` in §5, and **one writer for both**, `RecordAlertQuiet`, taking
the kind the way `RecordServerDisk` does. Inbox-only rather than subscribable by
§7's test: "your rule stopped watching" is governance news for the person who
wrote it, not an observed transition of a resource. And the generic
`inbox.Service.Record` cannot carry either of them — it returns early without a
`ProjectID` (`core/inbox/inbox.go`) — so a **server** rule's item goes down the
panel-level path, to the owners and admins `RecordServerDisk` already reaches.

## 5. Resolve, and the flap guard

**The recovery event is symmetric.** `alert.resolved` fires when every bucket
covering the last `window_seconds` is strictly below the threshold, with no gaps
— same window, same coverage rule, same gap rule. **One threshold, not two:** the
obvious refinement is a hysteresis band (fire at 90, resolve at 80) and it is
rejected, because it doubles what an operator must reason about, it stops the
sentence being a sentence, and it puts the second number in the *value* dimension
when flapping happens in the *time* dimension, where the window is already the
defence. Disk-management §5's decision, for its reason: *"a 'warning' and a
'critical' level sounds more careful and is not."*

This is deliberately asymmetric with the autoscale rule, which does have a
deadband and a longer down-window ([app-scaling.md](app-scaling.md) §9.2), and
the asymmetry is correct: scaling in too early costs an outage, so it must be
reluctant; saying "fine again" too early costs one message.

**The re-arm delay.** After a resolve a rule cannot fire again for
`max(15 min, 2 × window_seconds)`, recorded in `rearm_until`. It keeps evaluating
and its state stays live; it just does not deliver, so a value sitting on the
threshold produces one message an hour at worst rather than one every ten
minutes. Derived, not configured: the right cooldown is a function of the window
the operator already chose, and asking twice is asking them to be consistent with
themselves. The autoscaler's cooldown *is* a dial because there the cost is a
real scaling action; here it is a duplicate sentence.

**Flapping.** Slow oscillation — a genuine episode every twenty minutes all
afternoon — walks straight through the re-arm delay, and is how a channel gets
muted. So a rule opening its **fourth episode inside a rolling 60 minutes** enters
`flapping`: it keeps evaluating, its state stays visible, `alert_events` rows are
still written with `delivered = false`, and it delivers nothing until it has held
one state for an hour. Entering `flapping` writes one inbox item — the second of
§4's two new kinds, `alert.flapping`, through the same `RecordAlertQuiet`:
*"this rule has fired 4 times in the last hour and has been held until it
settles — its threshold or its window is probably too tight."* This is the one place the feature
deliberately stops telling an operator something, so the reasoning is explicit:
the alternative is not more information, it is a muted channel, and a muted
channel loses the *next* alert too. Holding the rule keeps the information where
the state is and drops the repetition where repetition does the damage — and the
one message that goes out names the fix (ui-principles §1).

## 6. The backtest

Before **Create alert** does anything, the panel has already said what the rule
would have done: *"Would have fired twice in the last 7 days — shown before you
save."*

**Why this is the feature.** Every alerting product treats "what number should I
type" as the operator's problem and hands them nothing to solve it with. We are
unusually placed: the rule reads a stored series and we already keep 14 days of
exactly that series (`CYPHERD_METRICS_RETENTION`), so "what would this have done"
is one indexed range scan over data already on disk. It is the cheapest
high-value thing in this spec and it is what stops a threshold being superstition.

**One evaluator, or the number is a lie.** The backtest and the live loop call
the same Go function — not the same algorithm written twice:

```go
// core/alerts
func Evaluate(rule domain.AlertRule, series []Bucket, now time.Time) []Episode
```

Pure, no I/O, no clock of its own. The live loop passes the buckets covering the
last window; the backtest passes seven days. Everything in §4 and §5 lives inside
it — coverage floor, gaps, re-arm delay, flap suppression — so every `Episode` it
returns carries its own `delivered` flag, exactly as an `alert_events` row does.
**The response carries both counts and the sentence quotes the delivered one**,
because that is the number of messages the operator would have received; under
the flap guard the two diverge by construction (§5), and a strip that reported
only episodes would promise pages that the guard would have held. If the
evaluator and the live loop ever diverge the backtest is worse than nothing: it would
teach an operator to trust a rule that behaves differently in production. So the
acceptance test is not "returns a plausible number" but "a rule live for seven
days, backtested, reproduces its own recorded `alert_events` row for row —
`delivered` flag included" (§13.4).

**What it says when it does not know.** The window is fixed at 7 days and is not
configurable, because the UI copy says 7 days and a configurable window makes that
copy a lie for whoever changed it. Two realities clamp it, both reported: if
retention is shorter or the target is newer, the response carries the span it
actually covered and the strip reads *"3 days of history — would have fired
once"*, never a confident zero over a window it did not have (ui-principles §10);
and a span containing sampled buckets (metrics-and-usage §4.5) yields an estimate
for `requests_per_second` and is labelled one. One honesty line belongs in the UI
and not only here: **a backtest is a statement about the past.** Two firings last
week is not a forecast — it is evidence that the number you typed is in the range
this target actually lives in. That is all it claims, and it is enough.

**Cost.** A read the caller triggers by typing, bounded at both ends: the client
debounces on change (400 ms), and the endpoint is one query returning at most
`7 × 24 × 12 = 2 016` buckets (168 for an hourly signal) over the index the
metrics endpoints already use. Nothing is written and nothing delivered, and the
draft need not exist first — the [notifications.md](notifications.md) "testing a
configuration before it is saved" pattern, for the reason stated there: *a dialog
that can only test what it has already stored teaches operators to save broken
things and find out later.* Unlike that path it dials nothing and touches only
rows the caller can already read, so none of its three narrowing rules apply.

## 7. Delivery, and the panel-scope debt it pays

**The events.** Two entries join the **subscribable** taxonomy in
`core/domain/notify.go`: `alert.firing` (error) and `alert.resolved` (info). One
edit to `domain.eventTypes` gives notifiers, outbound webhooks and the inbox all
three at once — the property that one-place taxonomy exists to have, and the move
[deployment-control.md](deployment-control.md) §5 made for `app.crashed`. For an
**application** rule that is the whole story. For a **server** rule the one edit
buys taxonomy membership only, because all three delivery paths are keyed on an
environment; the rest of this section pays for the notifier one and §14 says what
the webhook one still costs.
Subscribable rather than inbox-only by that section's test: an observed
transition of a resource, not governance news for named people. The message is
the sentence, which is the dividend the rule builder pays:

```
alert.firing    memory on srv-frankfurt-1 is above 90%
                Sustained for 5 min. Peak 96% at 14:35 UTC.
                Rule: when memory on srv-frankfurt-1 stays above 90% for 5 min.

alert.resolved  memory on srv-frankfurt-1 is back below 90%
                Fired for 42 min.
```

**The structural problem.** The design screen's example targets a **server**, and
a Notifier is scoped to a **Project** with a `NOT NULL` foreign key
(`0009_notifications.sql`). A Server belongs to no project, so the headline
example has nowhere to deliver to. This is not a new discovery —
`core/domain/inbox.go` says it about `server.disk_low`: *"Channel delivery for it
therefore waits on panel-level notifiers, which do not exist; that is a real gap
and it is named here rather than papered over."* Three ways around it, two
rejected: **deliver a server's alerts to every project's notifiers** (that same
comment already rejects it, and it leaks one team's host load into another team's
channel); **attach servers to a team** (servers are panel-level infrastructure
everywhere in the codebase — a tenancy-model change needing an ADR, not a
paragraph here); **give the Alert Rule its own channel config** (the second
delivery path: a second place sealing credentials, a second masked hint, a second
test endpoint, two SMTP senders — rejected outright).

So this spec pays the debt: **notifiers gain a panel scope.**

```sql
ALTER TABLE notifiers ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE notifiers ADD COLUMN scope TEXT NOT NULL DEFAULT 'project'  -- project | panel
  CHECK ((scope = 'project' AND project_id IS NOT NULL)
      OR (scope = 'panel'   AND project_id IS NULL));

-- 0009_notifications.sql's UNIQUE (project_id, name) is what stops two notifiers
-- in one project sharing a name. Postgres treats NULLs as distinct, so the
-- moment project_id is nullable that constraint stops nothing for panel rows:
-- two panel notifiers called "team-alerts" would both be legal, which is exactly
-- the double-add §2 argues against. The partial index restores it.
CREATE UNIQUE INDEX notifiers_panel_name ON notifiers (name) WHERE project_id IS NULL;
```

`idx_notifiers_project` stays as it is and simply no longer covers panel rows —
they are listed by scope, and there are few enough of them that the scan is the
right plan.

**What is unchanged, and what is not.** A Notifier keeps its four channels, its
sealed config, its `config_hint` and its `POST /notifiers/{id}/test`. The *row* is
a small change; the **delivery path is not**, and calling this a migration would
be understating it. Every existing path is environment-keyed end to end:
`notify.Manager.dispatch(ctx, envID, ev)` (`core/notify/notify.go`) resolves
environment → project before it does anything else and then asks
`Store.ListEnabledNotifiersForEvent(ctx, projectID, eventType)`
(`core/store/notifiers.go`), whose SQL filters `project_id = $1` — a row with
`project_id IS NULL` can never come back from it. `core/inbox.Service.Record`
returns early unless `ev.ProjectID != ""` (`core/inbox/inbox.go`). And
`domain.Notifier.ProjectID` is a non-pointer `string` (`core/domain/notify.go`),
so nullability is a domain, store and DTO change as well as a migration. A server
alert has no environment id, so today there is no entry point to dispatch it
from at all.

So this PR adds a **second entry point beside the environment-keyed one**: a
panel dispatch that takes no environment, a
`ListEnabledPanelNotifiersForEvent(ctx, eventType)` beside the project-scoped
query, and a nullable project on the domain type and the DTO. Everything past
that point — sealing, rendering, the four channel senders, the fan-out, the
inbox write — is the code that already exists, and the two entry points converge
on it after two resolutions. That is the honest size of the debt this section
pays.

A panel notifier is created and read by a **panel admin** (`requirePanelRole`, the rank that already
gates servers) and may subscribe to the alert events plus the panel-level kinds
that already fire and have never had a channel: `server.disk_low`,
`server.disk_recovered`, `panel.update_available`. That last clause is the only
thing here not about thresholds, and it is included rather than deferred because
a panel notifier that could not carry the three panel-level events that already
exist would be built for one subscriber; it costs a validation list, not a
mechanism. It also answers disk-management §8's *"per-server thresholds — one
panel-wide number until someone has a fleet heterogeneous enough to need two"*:
`CYPHERD_DISK_WARN_PERCENT` stays as the zero-configuration default for the
operator who never opens this screen, a rule is the per-server refinement, and an
operator who wants only rules sets that variable to `0`, which it already
documents as disabling it. **The down migration deletes panel-scoped notifiers**
— they cannot be represented in the old shape. That is within ENGINEERING rule
16 (it rolls back, and what it loses is rows that exist only because the feature
does), but it is a real statement and it belongs in the migration comment.

**One documentation correction rides along** (CLAUDE.md rule 7, one topic one
home). [disk-management.md](disk-management.md) §5 still says `server.disk_low`
and `server.disk_recovered` *"join the **subscribable** event taxonomy, beside
`app.crashed`"*, which the shipped code contradicts: `domain.eventTypes` holds
six entries and neither of these is among them, `core/domain/inbox.go` lists both
in `panelInboxKinds` with the reason in a comment, and `core/inbox/inbox_test.go`
asserts a panel-level kind is not a valid event type. That sentence predates the
structural gap this section closes, and this is the PR that
finally gives those kinds a channel — so it fixes the sentence in the same change
rather than leaving one fact with two homes.

**One rule about which notifier a rule may name:** *an application rule names a
notifier in its own project; a server rule names a panel notifier.* No matrix,
and it falls out of authorization — letting a project member aim their rule at a
panel notifier would push into a channel panel admins own that carries fleet-wide
news. It also makes **the audience of an alert its notifier's scope by
construction**, which is why `alert_rules` stores no `project_id`: an application
rule reaches the project's members, a server rule reaches panel owners and
admins, as `server.disk_low` does. The cost is small and real — a solo operator
wanting one Discord webhook for everything creates two notifiers pointing at the
same URL — against a rule needing no second sentence to explain it.

## 8. Evaluation on the plane

One goroutine with an owner, a cancellation path and observable failure
(ENGINEERING rule 7) — the shape `previews.RunSweeper` and
`scheduler.RunBackupSweeper` already use.

It ticks at the bucket boundary plus `CYPHERD_ALERT_EVAL_LAG` (default 60 s),
because agents seal and publish at the boundary and a report in flight is not a
gap (§4). Per tick it groups enabled rules by `(target_kind, target_id, signal)`
— many rules on one target read one series — and runs `Evaluate` per rule: for a
hundred rules, at most a hundred indexed range scans of a few dozen rows, once
every five minutes. Evaluating on each arriving `MetricsReport` instead would
re-run every rule once per server per bucket against a span still being filled in;
the periodic tick reads a settled one.

**The latency budget, plainly:** `window + bucket_seconds + eval_lag`, **11
minutes at the defaults**. This is not a seconds-scale pager and must not be sold
as one — `app.crashed` covers "it died" and fires within a heartbeat. This covers
"it is getting worse", where eleven minutes is the difference between noticing on
Tuesday afternoon and noticing on Wednesday morning.

Three silences, each of which says why it is silent. **When the plane is down**
nothing is evaluated and it cannot alert about its own absence; the resulting gap
is correctly read as `no_data` rather than all-clear, and a dead-man's switch is
out of scope (§14) rather than implied. The same asymmetry has a quieter half
worth naming: **the plane's own host is not a target.** It runs no agent, so it
has no `resource_metrics` row and `target_kind = 'server'` cannot name it — even
though the panel already reports that host's data-directory disk on
`GET /panel/version` (disk-management.md §6). A rule about the box the panel runs
on is the dead-man's-switch problem wearing a different hat, and it is deferred
with it. **When metrics collection is off**
(`MetricsSettings.enabled = false`) every rule is `no_data` and the screen says
*"metric collection is off"* once, at the top, with a link — not forty rows each
saying "no data" and leaving the operator to work out why (ui-principles §1: an
error offers its most likely remedy). **When a rule's notifier is disabled** the
rule still evaluates and its state is still true; the row shows "notifier
disabled".

## 9. Schema, the wire, and configuration

**Migration `0042_threshold_alerts.sql`.** The highest on disk is
`0039_server_disk.sql` (39 files). Nothing above it is actually reserved:
`0040` is claimed **twice** by unlanded specs — metrics-and-usage §6 and
app-scaling §9 both name it — and app-scaling names `0041` as well. So the number
follows merge order and the PR takes the next free one at merge time; `0042` is
what that is if both of those land first. It contains `alert_rules`, `alert_events`, the `notifiers`
scope change (§7), and — if metrics-and-usage has already landed — the three
additive columns on `resource_metrics` from §3.2. Reversible (rule 16), with the
stated loss on the notifier down-path.

**The wire is three additive proto fields and nothing else**:
`cpu_limit_millicores`, `disk_total_bytes` and `disk_used_bytes` on
`ResourceMetricBucket`, in the free numbers after that message's last. No new
message, no new subject, **no new verb** — nothing here asks an agent to do
anything, which is what keeps it inside ADR-005. `buf breaking` stays clean
(rule 18).

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_ALERT_EVAL_LAG` | `60s` | How long after a bucket boundary the evaluator reads, so a report in flight is not read as a gap. |
| `CYPHERD_ALERT_EVENT_RETENTION` | `90d` | How long episodes are kept. `0` = forever. Swept **hourly** in bounded batches by one owned goroutine — the shape audit retention already runs (`auditPurgeInterval = time.Hour`, `core/cmd/cypherd/main.go`), for its reason: the horizon is measured in days, so a tighter loop would query for nothing all day. |

Everything else is a constant with a stated value, not a dial: the coverage floor
(half a bucket), the re-arm delay (`max(15 min, 2 × window)`), the flap threshold
(4 episodes in a rolling hour) and the backtest window (7 days, because the copy
says 7 days). Each is a number an operator would have to reverse-engineer this
document to choose well, and offering the choice offers them the chance to break
the feature quietly.

## 10. API surface and authorization

Ten operations. The contract is **198** today and **206** after
metrics-and-usage, so **216** in that order — OpenAPI edited first, handlers
following it (rule 19).

| Route | Ability | Rank | Notes |
|---|---|---|---|
| `GET /api/v1/alert-rules[?target_kind=&target_id=]` | `read` | member | Their teams' application rules, plus server rules for a panel admin |
| `POST /api/v1/alert-rules` | `write` / `servers` | member / panel admin | §3.3 refusals; ability and rank follow the target |
| `GET /api/v1/alert-rules/{id}` | `read` | member | Rule, state, last firing |
| `PATCH /api/v1/alert-rules/{id}` | `write` / `servers` | member / panel admin | threshold, window, notifier, enabled |
| `DELETE /api/v1/alert-rules/{id}` | `write` / `servers` | member / panel admin | |
| `POST /api/v1/alert-rules/backtest` | `read` | member | An unsaved draft; returns episodes, the span covered, whether sampled |
| `GET /api/v1/alert-rules/{id}/events?window=` | `read` | member | Episode history |
| `GET /api/v1/panel/notifiers` | `admin` | panel admin | |
| `POST /api/v1/panel/notifiers` | `admin` | panel admin | |
| `POST /api/v1/panel/notifiers/test` | `admin` | panel admin | The unsaved-config test, unchanged — including its refusals |

`GET/PATCH/DELETE /notifiers/{id}` and `POST /notifiers/{id}/test` are **not**
duplicated for panel scope — they are addressed by id and only their
authorization branch changes, so there are three new notifier operations rather
than seven. Alert rules are a **flat collection, not one nested per target**,
because the operator's question is "what am I watching" across a fleet — one
screen, one request, the way `/usage` answers a cross-cutting question. A rule in
a project you are not in is absent from the list and `404` by id, the non-answer
every project-scoped route gives (`authz.go`). `window` takes the forms `?since=`
already takes ([deployment-control.md](deployment-control.md) §4), anything else
a `400` naming both.

**"Unchanged" on that last row carries a constraint, so it is stated rather than
discovered.** The existing unsaved-config test (`testNotifierConfig`) **refuses
the `email` channel with a 400** and requires a webhook URL to be `https` with a
dotted hostname, because that path can be aimed anywhere, repeatedly, and leaves
no row behind (notifications.md, *"testing a configuration before it is saved"*).
The panel-scoped twin inherits both narrowings unchanged — so a panel **email**
notifier, a likely choice for fleet alerts, cannot be proven before it is saved;
the operator saves it and uses `POST /notifiers/{id}/test`, which has a row to
unseal and therefore none of those limits. That is the right trade and it is not
obvious from the button, so the modal says it.

**Audit.** `alert_rule.created`, `alert_rule.updated` and `alert_rule.deleted`
join the closed vocabulary, which lives in **two** places in
`core/api/rest/openapi.yaml` and needs `alert_rule` added to both: the
`AuditEvent.action` family list (`auth`, `token`, … `notifier`,
`webhook_endpoint`, `panel`) and the `AuditResource.kind` glossary-noun list.
`core/audit` refuses a verb outside the vocabulary (audit-log.md §13
acceptance 10, `TestRecordRefusesAnUnknownAction`), so a handler shipped ahead of
that edit fails its own test rather than writing a row nobody can filter.
Deleting an alert rule is the "take the battery out
of the smoke detector" action and is exactly what
[audit-log.md](audit-log.md) exists to record. A *firing* is not audited: no
principal did it, and `alert_events` is its record.

## 11. Screens

The design screen is the source of truth for layout and copy.

**The modal** (`Add alert`), modal depth 1 per ui-principles §4:

> **New threshold alert**
>
> When `memory ▾` on `srv-frankfurt-1 ▾` stays above `90%` for `5 min ▾`
> notify `team-alerts ▾`
>
> ● *Would have fired twice in the last 7 days — shown before you save.*
>
> `Cancel` · **Create alert**

Five controls inline in a wrapping sentence — four selects and one value field,
which carries the emphasised border the design gives it because it is the only
thing the operator actually has to decide. Under it sits the plain-language line
ui-principles §11 requires, naming the denominator: *"% of srv-frankfurt-1's
16 GB of memory"* / *"% of one CPU core"*. The backtest strip is the design's
amber dot and its sentence, with four states of its own: a skeleton at the
strip's exact height while the debounce settles, so nothing shifts when the
sentence lands; the sentence in three shades (*"would not have fired"*, *"would
have fired twice"*, the warning shade at four or more); *"3 days of history"*
when retention is short; and an explicit "no data for this target yet" rather
than a confident zero.

**Settings → Alerts** (`web/src/routes/_app/settings/alerts.tsx`, breadcrumb
`SETTINGS / ALERTS`): one row per rule rendering its own sentence, its state,
when it last fired and how often this week.

**The four rule states are a new vocabulary, and that is a deliberate
exception.** ui-principles §5 has exactly one — `running` · `deploying` ·
`stopped` · `error` · `degraded` · `unknown` — and "one vocabulary, everywhere"
is the point of it. None of `ok` / `firing` / `no_data` / `flapping` is in that
list, and none of them should be: §5's words describe *a resource's health*, and
a rule is not a resource. A `firing` rule is working perfectly, so calling it
`error` would say the rule is broken when the thing it watches is; `no_data` and
`unknown` differ in exactly the way §4 spends its budget on. Reusing the nouns
would be worse than adding four. The exception is bounded the way app-scaling §8
bounded its refusal to add one: these words appear on alert rows and nowhere
else, they never describe an Application, a Server or a Database, and they borrow
§5's **colours** — which describe the watched thing, not the rule — so the
across-the-room scan still reads: `ok` green, `firing` red, `flapping` amber,
`no_data` hollow gray.

A **fifth top-level nav item was considered and rejected** — §4 makes a new
one a recorded decision competing with all four existing ones, and this is a
surface an operator opens when they set it up and when something pages them.
**Application → Traffic** and **Server detail** each gain an `Add alert` action
beside the metric, opening the same modal with target and signal pre-filled: that
is where the intent forms, because nobody decides to write an alert in Settings,
they decide it while staring at a graph.

## 12. Alternatives considered

- **Evaluate on the agent, next to the data.** Where the samples are, no plane
  round trip, sub-minute detection. Rejected three times over: the rules would
  have to be pushed as desired state, turning a plane-internal reaction into an
  agent feature with a reconciliation story; a rule targeting an application
  cannot be evaluated on one node once it has replicas on two
  ([app-scaling.md](app-scaling.md)); and the backtest needs the *stored history*,
  which only the plane has. Faster alerts about fewer questions, with no way to
  choose their thresholds.
- **Reuse the autoscale rule's table and controller** (`application_autoscale`,
  app-scaling §9.1). They rhyme — signal, threshold, sustained window, a brake —
  and differ in everything that matters. That one is per-application and singular
  ("one rule, one signal, one replica at a time"); this one is fleet-wide and
  plural. That one writes desired state and needs a deadband because its mistakes
  cost an outage; this one sends a sentence. That one has no notifier at all. What
  they share is the series read and the bucket arithmetic, and that is shared as
  code, which costs nothing.
- **A generic query-and-alert language** — PromQL-shaped expressions over
  arbitrary aggregations. The right answer for a monitoring product and the wrong
  one here, for metrics-and-usage §10's reason: fixed endpoints answer the
  questions the screens ask, and a query engine inside a control plane with a
  300 MB budget is a different product. A self-hoster who wants it runs Prometheus
  from the catalog.
- **Alert on the raw current value, per heartbeat.** Cheap, and it is what
  `server.disk_low` does. Rejected as the general mechanism because a heartbeat is
  not a stored series: it cannot be backtested, cannot express "for 5 minutes",
  and cannot answer p95 at all. `server.disk_low` stays precisely because it needs
  none of those. What that path gets right is borrowed wholesale rather than
  re-derived: `core/status.Recorder.checkDisk` alerts on the **transition** and
  latches the answer in a stored bit (`servers.disk_low`), which is why a channel
  is never told the same thing twice. `state` / `state_since` on `alert_rules`
  (§2, §5) is that same latch with four states instead of two and a clock on it.

## 13. Acceptance (testable)

1. A container held above 90% memory for two consecutive buckets → exactly one
   `alert.firing`, whose `peak_value` matches the worse bucket's
   `memory_bytes_peak`.
2. One bucket above the threshold, the next below → nothing delivered, no
   `alert_events` row.
3. Kill the agent mid-window so a bucket covers 40 s → the rule reports
   `no_data`, not `firing` and not `ok`.
4. **The backtest reproduces history.** Run a rule live for seven days, then
   backtest the identical rule: the returned episodes match the recorded
   `alert_events` **row for row** — same count, same `started_at`, same
   `delivered` flag — so the delivered subset equals the messages actually sent.
   The feature's central claim rests on this.
5. Backtest with `CYPHERD_METRICS_RETENTION=3d` → the response reports a 3-day
   span and the UI says so; it never reports zero firings over 7 days.
6. Cross the threshold every 10 minutes for an hour → at most two deliveries, the
   rule enters `flapping` on the fourth episode, one inbox item is written, and
   later `alert_events` carry `delivered = false`.
7. Return to normal for one window → exactly one `alert.resolved`, whose body
   states the episode's duration.
8. A rule on an application in project A naming a notifier in project B → `400`.
   A server rule naming a project notifier → `400`.
9. Delete a notifier a rule uses → `409` whose body names the rules. Delete the
   application a rule targets → rule and events gone, `alert_rule.deleted`
   audited.
10. `MetricsSettings.enabled = false` → every rule is `no_data` with that reason,
    nothing delivered, no rule sitting at zero.
11. A member of team A calls `GET /alert-rules` → team B's rules are absent, and
    every server rule is absent unless the caller is a panel admin.
12. One hundred rules across ten servers → one tick issues at most one series read
    per `(target, signal)` pair, verified by query count.

## 14. Deliberately out of scope

- **"Stays below" and every other comparator.** Genuinely wanted — *"requests
  dropped to zero"* is a real alert — and not here, because a below-threshold rule
  on `requests_per_second` cannot distinguish "no traffic" from "no data", the
  exact distinction §4 spends its whole budget establishing. That needs resolving
  first, and the sentence needs a comparator slot it does not have.
- **Multi-condition rules** (`cpu > 90 AND memory > 90`) and rules across more
  than one target. The first turns the sentence into a form; the second needs an
  aggregation policy (all? any? the worst?) nobody can guess from the UI.
- **Anomaly detection and dynamic baselines.** A much larger product; the backtest
  is the deliberately dumb version and it works *because* the operator still
  chooses the number.
- **Auto-remediation.** "Restart it when memory is high" is not an alert, it is a
  controller with a blast radius, and ADR-005 requires it be expressible as
  desired state. The autoscale rule ([app-scaling.md](app-scaling.md) §9) is that
  feature, with its own brakes for its own reasons.
- **Escalation policies, on-call rotations, acknowledgement.** PagerDuty and
  Opsgenie are a channel away (notifications §9), but a rotation is a different
  product in a different tool, and a panel that half-implements one is worse than
  a panel that integrates with the real one.
- **Silences and maintenance windows.** Wanted, and when they arrive they should
  be the weekly, zone-aware, week-wrapping shape
  [deploy-protection.md](deploy-protection.md) already built for freeze windows —
  not a second scheduling model. Disabling the rule is today's blunt version.
- **Alert rules on Managed Databases and Compose Stacks.** Identical buckets, and
  the rule shape works unchanged; `target_kind` is polymorphic from day one so it
  is a validation list and a picker entry, not a migration. Two targets ship first
  because they exercise the two different notifier scopes, which is the part that
  is actually new.
- **Status alerts** — "page me when this is `degraded`", which app-scaling §8 says
  an operator wanting to hear about partial replica loss is really asking for. A
  transition on a status column, not a threshold on a series; it belongs to the
  `app.*` taxonomy, not to this evaluator.
- **A dead-man's switch for the control plane, and rules on the plane's own
  host.** The plane cannot alert about its own death (§8); the honest fix is an
  outbound heartbeat to something external, which is its own small feature and
  not a threshold rule. The same gap covers the box `cypherd` runs on: it is the
  one node with no agent and no `resource_metrics` row, so `target_kind =
  'server'` cannot name it, even though `GET /panel/version` reports its
  data-directory disk today (disk-management.md §6).
- **Per-rule delivery history and retries.** Alerts ride the notify path, which
  is fire-and-forget by design; the machine-facing twin with delivery ids,
  attempts and replay already exists — [outbound-webhooks.md](outbound-webhooks.md).
  An **application** rule's events join it for the price of §7's taxonomy edit. A
  **server** rule's do not, and the reason is §7's shape again:
  `webhooks.Manager.resolve(base, envID, eventType)` (`core/webhooks/webhooks.go`)
  is environment-keyed like `notify.dispatch`, and a delivery row's resource kind
  is a closed two-value set — `domain.WebhookResourceApplication` /
  `WebhookResourceDatabase` (`core/domain/webhooks.go`) — with no `server` member.
  A panel-scoped endpoint and a third resource kind are that spec's change to
  make, not this one's; until then a server rule delivers through a panel
  notifier and the inbox, which is what §7 builds.

## Decisions taken (orchestrator, not the spec author)

**The panel notifier path is in scope for this feature, not deferred.** The
auditor was right that a nullable `ProjectID` across domain, store and DTO plus a
panel dispatch is a genuine second entry point rather than a small migration.
Building the rule engine on a delivery path that cannot reach a server-scoped
alert would ship a feature that half works, so the notifier change lands here.

**Outbound webhooks for server-scoped alerts stay deferred**, and the launch
state is notifier plus inbox. `webhooks.Manager.resolve` keys on an environment,
and giving it a panel scope and a third resource kind is a change to
`outbound-webhooks.md`'s own model — a different feature's decision to take.

**The four rule-state colours are checked against the design screen at
implementation time**, not accepted from the spec. §11 names the screen as the
source of truth for colour, so the screen wins over a proposal.
