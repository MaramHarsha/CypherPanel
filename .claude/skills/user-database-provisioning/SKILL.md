---
name: user-database-provisioning
description: MariaDB database/user/grant provisioning for hosting accounts, credential handling, and phpMyAdmin/Adminer handoff. Use when working on the user-facing database management surface.
---

# User Database Provisioning

> **Status: design-intent (pre-implementation).** Grounded in plan.md Section 4B (Database Management) and the MVP-default decision (MariaDB). Lands in Phase 4. Verify against code then, updating in the same PR. Read [[jobs-and-agent-tasks]], [[auth-and-rbac]], and [[database-and-migrations]] (note: that skill is about CypherCore's *own* Postgres; this is about *hosted-account* MariaDB — different concern).

## MVP default & adapter

- **MariaDB** is the MVP default for user databases (what shared-hosting PHP apps expect). Provisioning goes through an adapter interface so a **PostgreSQL** user-DB adapter can drop in post-MVP without touching the feature/API layer. Design operations as an interface (`CreateDatabase`, `CreateUser`, `Grant`, `Drop`) from the start.
- This is separate from CypherCore's control-plane Postgres — never conflate the two.

## Provisioning conventions

- Database and DB-user creation runs as an **idempotent agent task** (see [[jobs-and-agent-tasks]]) on the account's server — re-running converges (create-if-not-exists), never errors on a second delivery.
- Namespacing: prefix account databases/users to prevent collisions across accounts and make ownership obvious; enforce the account's package `databases` limit before creating.
- **Grants are least-privilege**: a DB user gets rights only on its own databases, scoped to the correct host. Never issue global privileges to an account's DB user.

## Credentials

- Generate strong random passwords; **never log them** and never put them in task payloads that land in the audit trail or stream storage (payloads must be secret-free — see [[jobs-and-agent-tasks]]). Deliver credentials to the user through the authenticated API response / UI once, or store hashed/encrypted per the credential-storage approach, not in plaintext columns.

## phpMyAdmin / Adminer handoff

- One-click access performs an authenticated session handoff scoped to the account's own databases — the tool must not expose other accounts' data. Route/proxy it per-account; don't hand out a shared admin session.
