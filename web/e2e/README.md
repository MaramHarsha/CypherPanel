# Browser regression tests

These drive the **real panel** — the built UI served by a real `cypherd` against
a real PostgreSQL — through a real browser. Nothing is mocked.

## Why they exist

An external review on 2026-09-07 found five defects on a branch whose sixteen CI
jobs were all green. Four of them were the same shape: a capability the
database, the store, the scheduler and the agent all supported, with **no way to
reach it from a screen**. The GitHub App could be connected and no application
could use it. A push verified and deployed nothing. A repository the create
dialog itself suggested could not be cloned.

Backend integration tests cannot see any of that, because they drive the API
directly and the gap is between the API and the UI. `scripts/api-ui-parity.py`
catches part of it mechanically, and its own limitation is now documented: it
compares the OpenAPI spec against the screens, so a capability missing from the
spec is invisible to it — which is exactly how `github_installation_id`
survived.

A browser test has neither blind spot. It is the only check here that fails when
a control is missing, disabled, or wired to the wrong field.

## Running them

```
make e2e            # boots a throwaway panel, runs the suite, tears it down
```

The harness needs Docker (for PostgreSQL and the Playwright image) and a built
`cypherd` with the UI embedded — `make e2e` does all of it.

**On a host that already runs a panel**, the default is refused: two agents on
one machine converge the same `cypher-proxy` container and fight over Traefik's
configuration directory, so a run here would disturb whatever the live one is
serving. Use `E2E_AGENT=enroll`, which enrols an agent and stops there — the
`enroll` subcommand talks to the plane and writes certificates, touching no
Docker and converging nothing. Every create dialog filters on `server.enrolled`
rather than on a running status, so the whole suite still runs.

```
E2E_SKIP_BUILD=1 E2E_AGENT=enroll ./scripts/e2e.sh     # binaries already in .e2e/
```

`E2E_AGENT=none` boots the panel alone. `E2E_SKIP_BUILD=1` reuses the binaries in
`.e2e/`, for a machine whose Go and pnpm live in containers — build them there
first, and **do not swallow the build's output**: `pnpm build` is `tsc -b &&
vite build`, so a type error leaves the previous bundle in place and the suite
then tests the old one and passes. That happened while writing these.

## What they cover, and what they deliberately do not

They cover the flows where a missing control is invisible to every other test:
signing in, creating a project and an application, the repository field's
validation, the private-repository credential pickers, and what the panel offers
when there is no server to deploy to.

`golden-path.spec.ts` is the one that was not written after a report. Every
other file here exists because somebody hit a missing control and said so; a new
operator who gets stuck on step two of four does not file a bug, they close the
tab. It also exercises the guided-onboarding band, whose progress is DERIVED
rather than stored (guided-onboarding.md §4) — which means it goes silently
wrong the moment a creation path stops being counted, and nothing below a
browser can see that.

**Every spec here was checked by breaking the thing it watches.** A test that
passes against a broken panel is worse than no test, and one of these did:
`golden-path` went green with the onboarding band deleted, because the build
that was supposed to remove it had failed and the harness reused the old bundle.

They do **not** run a deploy. That needs a Docker daemon, a builder and a
registry-free image relay, and `integration.yml`'s Deploy slice already proves
it end to end against real Docker and real Traefik. Duplicating it here would
buy a slower, flakier copy of a test that exists.
