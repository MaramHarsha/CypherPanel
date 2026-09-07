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

## What they cover, and what they deliberately do not

They cover the flows where a missing control is invisible to every other test:
signing in, creating a project and an application, the repository field's
validation, the private-repository credential pickers, and what the panel offers
when there is no server to deploy to.

They do **not** run a deploy. That needs a Docker daemon, a builder and a
registry-free image relay, and `integration.yml`'s Deploy slice already proves
it end to end against real Docker and real Traefik. Duplicating it here would
buy a slower, flakier copy of a test that exists.
