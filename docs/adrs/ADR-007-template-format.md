# ADR-007: A native declarative template schema, with a Coolify compose importer

- **Status:** Accepted
- **Date:** 2026-08-02

## Context

The template catalog is the headline of Phase 4
([roadmap.md](../roadmap.md)), and its acceptance gate is explicit: *a Coolify
or Dokploy user can self-migrate a typical workload without losing a capability
they used*. Coolify ships ~361 compose-YAML templates with "magic" environment
variables (`SERVICE_FQDN_*`, `SERVICE_PASSWORD_*`,
[research/coolify.md](../../research/coolify.md)); Dokploy fetches a versioned
template index from a remote registry ([research/dokploy.md](../../research/dokploy.md)).

Three candidates were on the table: adopt Coolify's compose-YAML + magic envs
as our native format, adopt Dokploy's remote registry, or design a merged
schema.

The decision is constrained by two things we have already committed to:

- **ADR-005 — everything is desired state.** A resource is a row the agent
  reconciles toward. There is no "run this compose file" verb, and adding one
  would reintroduce exactly the imperative path ADR-005 exists to remove.
- **The domain model is Application and Managed Database**, not "a set of
  containers". Routes, health gates, sealed env vars, backups, scheduled tasks
  and preview environments all hang off those two nouns. A template that
  produced anything else would produce resources the rest of the panel cannot
  manage.

Phase 4 separately lists **Compose Stack** as its own resource type. That is
the honest home for "I have a compose file and want it run as-is" — and it is
a different feature from "give me Plausible, configured properly, in one
click".

## Decision

**A native declarative template schema is the format. Coolify's compose+magic-env
templates are supported through an importer, not adopted as the native format.
The catalog ships inside the release rather than being fetched at runtime.**

Three parts:

1. **Native schema.** A template is a YAML document that declares the
   CypherPanel resources to create — Applications and Managed Databases with
   their images, ports, routes, health checks, volumes, env vars, and generated
   secrets. It resolves to ordinary rows. Once installed, a templated app is
   indistinguishable from a hand-made one: the same deploy pipeline, the same
   rollback, the same backups, the same danger zone. No template runtime, no
   second code path, nothing that only works because it came from a template.

2. **A Coolify importer.** Their compose+magic-env format is translated into
   the native schema — services become Applications or Managed Databases,
   `SERVICE_FQDN_*` becomes a route, `SERVICE_PASSWORD_*` becomes a generated
   sealed secret. Translation is a build-time/one-shot concern, so a template
   that cannot be expressed is rejected loudly with the reason, rather than
   half-imported into something that misbehaves later. This is what makes the
   361 reachable without letting their format dictate our data model.

3. **Bundled catalog.** Templates ship with the binary and are versioned with
   it. A remote index (Dokploy's model) can be added later behind the same
   schema, but it is not the starting point: a runtime fetch makes the catalog
   a network dependency and a supply-chain surface for something an operator
   installs with one click, and "the panel works offline" is worth more at this
   stage than "the catalog updates without an upgrade".

## Consequences

**Good.** Templates produce first-class resources, so every existing feature
works on them for free and there is no template-shaped hole in the product.
The importer gives us Coolify's library as *content* without inheriting their
schema as *contract* — we can change our internals without breaking templates,
and their format can drift without breaking us. Bundling keeps installation
hermetic and auditable.

**Costs, accepted.** The importer is real work and will not cover every
template — some Coolify templates lean on compose features (arbitrary service
graphs, custom networks) that only map onto a Compose Stack. Those wait for
that resource rather than being forced through this one. Catalog updates
require a release, which is a real regression against Dokploy's model; the
remote index is the escape hatch when the catalog's rate of change justifies
it.

**Explicitly not decided here.** The Compose Stack resource's own shape, and
whether it eventually shares the importer. This ADR only settles what a
*template* is.
