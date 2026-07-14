---
name: extensibility-and-events
description: Plugin manifest/permission model and the internal Event Bus conventions (plan.md §11-12). Use when reserving plugin surfaces, emitting/consuming domain events, or building anything plugins will hook.
---

# Extensibility & Events

> **Status: design-intent (pre-implementation).** Grounded in plan.md §11-12, not yet in code. The Phase 2 reservations (route namespace, `plugins` table, `events.>` subjects, manifest schema) come first; the runtime is post-MVP. Verify and expand this against real code as each piece lands, updating in the same PR.

## Event Bus (§12) — subject conventions

- Domain events publish to NATS JetStream under the **`events.>`** subject tree, kept strictly **separate from agent-task subjects** (`tasks.server.<id>`). Never overload one for the other — tasks are work-to-do (WorkQueue, one consumer), events are facts-that-happened (fan-out, many consumers).
- Event subjects are `events.<aggregate>.<verb>` past-tense: `events.account.created`, `events.account.suspended`, `events.domain.added`, `events.dns.record.changed`. Add new events by extending this namespace, never by renaming existing ones (consumers depend on them).
- **Direct call vs. event:** if the caller needs the result to proceed, it's a direct function/gRPC call (e.g. "create the DB row"). If other parts of the system merely need to *react* (send a webhook, write an analytics row, invalidate a cache), emit an event. Don't turn a required step into an event.
- Provide in-process pub/sub for same-binary subscribers alongside the JetStream publish, so a single Core instance doesn't round-trip through NATS for local reactions.
- Events are facts: include the aggregate ID and a minimal immutable snapshot, never secrets, and never mutable references the consumer would re-fetch inconsistently.

## Plugins (§11) — reservations first

- Reserve the **`/api/v1/plugins/`** route namespace and a `plugins` table (migration) before any loader exists, so the surface is stable.
- The plugin manifest (`plugin.yaml`) schema is **finalized before the first third-party plugin ships** — a manifest format that changes after plugins exist breaks the ecosystem. Manifest declares: identity/version, the API/UI surfaces it registers (sidebar entries, dashboard cards, settings pages), event subscriptions, and a **permission list** enforced against what it may call.
- Backend plugins run **process-isolated** with permissions enforced against the manifest (a plugin gets exactly what it declared, nothing ambient). UI plugin slots are registered from the manifest, not by editing core UI.
- Themes and language packs are plugin *types* layered on the white-labeling/i18n scaffolding, not special cases.
- The marketplace (hosted registry, signing/review) is deferred indefinitely and out of scope for the runtime.
