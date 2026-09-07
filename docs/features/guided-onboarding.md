# Feature spec: Guided onboarding

> The feature matrix's **V1** onboarding row: *"Ours is 4 steps: welcome → add
> server (join command or 'use this machine') → deploy app/template → live URL.
> ADR-002 deletes Coolify's SSH-key steps."* Coolify ships a 7-step boarding
> flow; Dokploy drops you on an empty dashboard and wishes you luck.
>
> Written 2026-09-07, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. What is actually missing

Not the screens. Every step of the golden path already exists and every one has
an empty state that names its own next action — "Join your first server",
"Create your first project" — which is ui-principles §11 working as intended.

What is missing is the **thread between them**. An operator who has just created
their owner account lands on an empty Projects page. Nothing on it mentions that
a project without a server cannot deploy, so the common first experience is:
create a project, create an environment, create an application, press Deploy,
and get told there is no server to run it on. Four steps of work before the
panel mentions the prerequisite.

So this feature is one thing: **say what is left, in order, until it is done.**

## 2. Progress is DERIVED, never stored

There is no `onboarding_completed` column and no dismissed flag, and that is the
whole design.

A stored flag has to be written by whoever completes a step, which means every
creation path has to remember to write it — the template installer, the preview
environment, the API, a future CLI. `dns-automation.md` §4.3 records exactly
this lesson after the first real use of DNS automation produced no record,
because a template install created an application through a path nobody had
hooked. Onboarding would fail the same way and more visibly: the panel would
still be asking you to add a server twenty minutes after you added one.

So the steps are read from what EXISTS, on request:

| Step | Complete when |
|---|---|
| `owner` | the panel has an account (it always does by the time anyone sees this) |
| `server` | any server has completed enrollment |
| `project` | any project exists |
| `deploy` | any deployment has reached `succeeded` |

Four queries, four counts. Nothing to migrate, nothing to backfill, and a panel
that has been running for a year answers "done" without ever having stored a
thing.

**It also self-heals in the direction that matters.** Delete every server and
the panel says you need one again — which is true, and a stored flag would have
lied.

## 3. Where it appears, and where it does not

**On the Projects page, above the list, until the last step is done.** Projects
is the landing page (ui-principles §4, decided 2026-07-17) and a home dashboard
is post-v1, so onboarding does not get a route of its own to become one. It is a
band on the page an operator already lands on, and it disappears — permanently,
because it is derived — when the fourth step completes.

It is **never a wall**. No modal, no redirect, no blocked navigation. An operator
who knows exactly what they are doing scrolls past it, and one who has a
half-configured panel from an earlier attempt is not trapped in a flow that
insists on a state they already have.

The one thing it may not do is reappear. A panel whose last server is
decommissioned during maintenance should not greet its owner with a beginner's
wizard, so the band renders only while **no deployment has ever succeeded** —
the step that proves the whole path worked once. After that the derivation still
answers, and nothing shows it.

## 4. The steps carry the actions, not links to them

Each step is its own control:

- **Add a server** opens the Join dialog — which now offers **Use this machine**
  ([local-server.md](local-server.md)), and that is the step this feature makes
  land: on a single-VPS install the first server is one click and no terminal.
- **Create a project** opens the same dialog the empty state does.
- **Deploy something** goes to the template catalog, because a first deploy from
  a template succeeds far more often than a first deploy from a repository —
  there is no build to get wrong.

A step whose prerequisite is unmet is shown but not actionable, with the reason:
"add a server first" is more useful than a button that opens a dialog which then
refuses.

## 5. API

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/onboarding` | member | the four steps, each with `complete`, and the counts behind them |

Member, not admin: everyone who can see the panel can see how far it is set up,
and the answer contains no credential and no name — four booleans and four
counts. What an individual step's *button* then does is gated by that action's
own rank, as it already is.

One route and one GET. There is deliberately no `POST /onboarding/dismiss`,
because there is nothing to dismiss: §2.

## 6. Deliberately out of scope

- **A checklist that survives completion.** Once the path is walked the band is
  gone for good. A permanent "getting started" panel is documentation, and the
  documentation site is where that lives.
- **Progress inside a step.** "Server enrolling…" is already on the Join dialog
  and the fleet table; repeating it here would be a second place to look.
- **Teaching the concepts.** The band names actions, not ideas. Projects,
  environments and desired state are explained in the guides, linked from the
  band's one footer line rather than paraphrased into it.
- **A separate `/welcome` route.** §3 — it would be a dashboard by another name,
  and ui-principles §4 already decided against one for v1.
