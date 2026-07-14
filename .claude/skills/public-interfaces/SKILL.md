---
name: public-interfaces
description: Conventions for everything built on top of the OpenAPI spec — SDKs (Go/Node/Python), cypherctl CLI, webhooks, and the billing-adapter contract (plan.md §14/§15/§18). Use when building any client, CLI, webhook, or integration layer.
---

# Public Interfaces

> **Status: partially grounded.** The OpenAPI spec and generated TS client exist today; SDKs, `cypherctl`, webhooks, and billing adapters are Phase 6 / post-MVP. Verify/expand against code as they land, updating in the same PR. Read [[api-contract-workflow]] first — the OpenAPI spec is the single source everything here derives from.

## Everything derives from the OpenAPI spec — nothing is hand-maintained

- The **Go, Node, and Python SDKs** (§14) are **generated** from `docs/swagger.json` (e.g. `oapi-codegen` for Go; the Node SDK shares the generator config with the CypherUI TS client). A hand-written parallel client is a bug — it will drift.
- `cypherctl` (§14) is a thin CLI over the same REST API (account/server/dns/backup/ssl commands, plus `upgrade`/`rollback` from [[upgrade-and-compatibility]]) — it calls endpoints, it does not re-implement business logic.
- When the API changes: regenerate spec → regenerate SDKs/client (same flow as [[api-contract-workflow]]). Never patch a generated client by hand.

## Webhooks (§15)

- Outbound webhooks deliver **domain events** (from the Event Bus — [[extensibility-and-events]]) to operator/user endpoints via a **dedicated JetStream consumer** (not inline in the request path).
- **Sign every delivery** (HMAC-SHA256 over the body with a per-subscription secret) so receivers can verify authenticity. Include a timestamp to bound replay.
- Retry with backoff and **dead-letter** after N attempts (same discipline as [[jobs-and-agent-tasks]]); provide a delivery log and **manual redelivery** in the UI. Never block a domain operation on webhook delivery.

## Billing adapter (§18)

- The billing integration is a **published adapter contract** generalized from the WHMCS module, so the community can build Blesta/HostBill adapters **without touching CypherCore**. Provisioning/suspension/termination are driven through the existing REST API — the adapter is an external consumer, not a core dependency.

## Principle

Every public interface is a thin, generated, or contract-defined layer over the existing REST + event surfaces. If you're tempted to add business logic *inside* an SDK, CLI, webhook, or adapter, it belongs in CypherCore behind an endpoint instead.
