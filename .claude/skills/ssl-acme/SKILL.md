---
name: ssl-acme
description: Let's Encrypt / ACME certificate issuance with the Lego library — challenge selection, storage, renewal, and reload coordination. Use when working on SSL issuance, renewal jobs, or cert storage.
---

# SSL / ACME

> **Status: design-intent (pre-implementation).** Grounded in plan.md (SSL Engine: Lego; CypherAgent SSL Issuance). Lands in Phase 3. Verify against code then, updating in the same PR. Read [[jobs-and-agent-tasks]] and [[agent-config-generators]] first.

## Library & challenge selection

- Use **Lego** (Go ACME library) — no shelling out to certbot. Supports Let's Encrypt and ZeroSSL (ACME) via the same client.
- **HTTP-01** for single hostnames pointing at this server (write the challenge token under the ACME webroot from `internal/paths.Layout.ACMEWebRoot`, served by the web server).
- **DNS-01** for wildcards and where HTTP-01 is impractical — solved via the DNS provider (PowerDNS locally, or the user's external provider). DNS-01 is required for `*.domain`.
- Issuance/renewal runs as an **idempotent agent task** (see [[jobs-and-agent-tasks]]): re-running when a valid cert already exists is a no-op, not a re-issue (respect rate limits).

## Storage & permissions

- Certs and keys go to well-known paths from the path layer, **not hardcoded**. Private keys are `0600`, owned appropriately, never world-readable and never committed (the repo's `*.key`/`*.pem` gitignore applies).
- Store issuance metadata (domains, issuer, notAfter) so renewal scheduling and the UI don't have to parse PEM on every request.

## Renewal & reload

- Schedule renewal well before expiry (e.g. at ~30 days remaining); renewal is the same task path as issuance.
- After issuance/renewal, **coordinate a web-server reload** (validate-then-reload per [[agent-config-generators]]) so the new cert is picked up — never restart, and never reload on a failed issuance.
- Surface failures (rate limits, DNS propagation, validation) as task errors with actionable messages; transient DNS propagation is retryable, a misconfigured domain is `jobs.Permanent`.
