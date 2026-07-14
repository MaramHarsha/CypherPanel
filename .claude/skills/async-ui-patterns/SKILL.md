---
name: async-ui-patterns
description: How CypherUI surfaces long-running provisioning jobs — status polling, pending states, empty/skeleton states, and destructive-action safety. Use when a UI action triggers an agent task or any async backend operation.
---

# Async UI Patterns

## The provisioning round-trip (established pattern)

Agent-executed operations are asynchronous: the API returns `202 Accepted` with a resource whose `status` starts at `provisioning`/`terminating`, and the agent flips it later (see the jobs-and-agent-tasks and auth-and-rbac skills for the backend half). The UI reflects this by:

- **Polling the list query** with `refetchInterval` while transitions are expected — accounts poll at 5s (fast enough to feel live for provisioning→active), dashboard/servers at 15s (heartbeat cadence). Keep intervals matched to how fast the underlying state actually changes.
- Rendering `status` as a `Badge` with a variant per state (active→default, provisioning/terminating→secondary, suspended→outline, failed→destructive). Never show a bare spinner that hides which state a resource is in.
- On mutation success, `invalidateQueries` the affected list so the new/changed row appears immediately, then let polling take over for the agent-driven transition.

## Loading & empty states (required, not optional)

- **Skeletons, not spinners**, for initial loads (`<Skeleton className="h-… rounded-…" />` in the same shape as the eventual content).
- **Design every list empty-first**: an icon, a one-line explanation, and the action that resolves it ("Create a package before provisioning accounts"). Empty states are a first-run onboarding surface, not an afterthought.

## Destructive-action safety

Irreversible actions (terminate account, delete) use **type-to-confirm**, not a bare "Are you sure?": an `AlertDialog` whose confirm button stays disabled until the user types the exact resource name (see `TerminateButton` in the accounts page). Suspend/unsuspend (reversible) can be one click.

## Mutations

Use `useMutation` with `isPending` to disable the submit control and show progress text ("Provisioning…"), and surface `ApiError.message` inline in the dialog rather than a toast that can be missed. Reset form + close dialog only in `onSuccess`.

## Future: live progress (not yet built)

Long jobs will stream progress via WebSocket/SSE into a persistent notification center (plan.md Section 5). Until that exists, polling is the sanctioned mechanism — when the notification center lands, migrate long operations to it and update this skill in the same PR.
