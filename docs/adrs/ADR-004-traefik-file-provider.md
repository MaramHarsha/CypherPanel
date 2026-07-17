# ADR-004: Traefik v3 via file provider, proxy behind a driver interface

- **Status:** Accepted
- **Date:** 2026-07-17

## Context

Both references converged on Traefik as the default reverse proxy (Coolify also offers Caddy). Coolify manages proxy config through actions on the control plane pushed over SSH (`coolify/app/Actions/Proxy/`, `bootstrap/helpers/proxy.php`); Dokploy generates Traefik config files from the manager node (`dokploy/packages/server/src/utils/traefik/`). Exposing Traefik's API for dynamic config is a known attack surface; generating files centrally and shipping them is racy.

## Decision

**Traefik v3 runs on every worker node**, and the **local cypher-agent owns its dynamic configuration** via Traefik's file provider — atomic writes (write temp file + rename) of per-resource config fragments. TLS via Let's Encrypt (HTTP-01 default, DNS-01 for wildcards), with support for user-supplied certificates. Proxy config generation sits behind a **driver interface** in `agent/proxy/`, exactly like orchestrator drivers.

## Alternatives considered

- **Caddy as primary.** Simpler config model and automatic HTTPS, but weaker middleware ecosystem and both reference codebases' porting value is Traefik-shaped. Deferred: it becomes the second proxy driver, giving Coolify feature parity.
- **Nginx.** No first-class dynamic config story without reload choreography. Rejected.
- **Exposing the Traefik API for dynamic config.** Rejected: unnecessary attack surface; file provider achieves the same result inert.

## Consequences

- Proxy changes are local to each node — no central config distribution step, no partial-fleet inconsistency during control-plane outages.
- The agent must own config hygiene: atomic writes, validation before activation, cleanup of fragments for deleted resources.
- Certificate material lives on the node that serves it (encrypted at rest), not in the control-plane database; the control plane holds only metadata and renewal schedules.
- Adding Caddy later is a bounded task: implement the `proxy.Driver` interface, port nothing else.
