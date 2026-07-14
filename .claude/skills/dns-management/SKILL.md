---
name: dns-management
description: PowerDNS zone/record management — REST API usage, per-record-type validation, and primary/secondary cluster sync. Use when working on the DNS zone editor or DNS clustering.
---

# DNS Management

> **Status: design-intent (pre-implementation).** Grounded in plan.md Sections 4B/5 (PowerDNS MVP default). Lands in Phase 5. Verify against code then, updating in the same PR. Read [[agent-config-generators]] and [[jobs-and-agent-tasks]].

## Engine (MVP default)

- **PowerDNS** is the MVP default, chosen specifically because it's **REST API-driven** — manage zones/records through the PowerDNS HTTP API, not by hand-editing BIND zone files. A **BIND** adapter comes post-MVP behind the same interface; design zone/record operations as an interface so it drops in.
- Prefer the API over generating zone files where PowerDNS offers it — but where a file/template is produced, validate (`pdnsutil check-zone`) before applying (see [[agent-config-generators]]).

## Record CRUD & validation

- Support the full record set from the zone editor: **A, AAAA, CNAME, MX, TXT, SRV, CAA** (plus NS/SOA management).
- **Validate per record type** before accepting: A/AAAA are valid IPv4/IPv6; CNAME targets are hostnames and **cannot coexist with other records at the same name** (including apex); MX/SRV have priority/weight/port fields; CAA has flag/tag/value; TXT length/escaping. Reject invalid records at the API boundary with a clear message rather than letting PowerDNS reject them opaquely.
- Normalize names (trailing dot / relative-to-zone) consistently; bump the zone **SOA serial** on every change.

## Clustering (primary/secondary sync)

- The DNS cluster sync engine keeps secondaries in step with the primary (AXFR/IXFR or PowerDNS native replication). Zone changes propagate to all nodes; a record edit isn't "done" until secondaries have it.
- Cross-node propagation runs through the agent task pipeline where a change must fan out to multiple servers — idempotent, retryable (see [[jobs-and-agent-tasks]]).
