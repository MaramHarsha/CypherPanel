# CypherPanel — Rules for AI / Contributors

> Hard boundaries for anyone (human or AI) writing code in this repo. These are enforced conventions, not suggestions — if a change requires breaking one, stop and flag it rather than working around it silently. Background/rationale for most of these lives in [Architecture.md](Architecture.md) and `plan.md`; per-subsystem detail lives in `.claude/skills/`.

## 0. Read before you write

**Always read the relevant `.claude/skills/<name>/SKILL.md` before writing code in that area.** There is one skill per subsystem (`go-backend-conventions`, `database-and-migrations`, `auth-and-rbac`, `ui-development`, etc.) — they encode the *current, shipped* pattern for that area, not aspirational design. If a skill's `Status: design-intent` line is still present for a phase that has since landed, flag the mismatch rather than trusting either blindly.

## 1. Libraries — required vs. forbidden

| Area | Use | Never use | Why |
|---|---|---|---|
| API framework | Gin | Fiber | fasthttp breaks net/http middleware/HTTP2 compatibility; the perf delta doesn't matter here |
| DB access | `pgx` + `sqlx`, hand-written SQL | GORM | Reflection-based mapping + default eager loading cause N+1s and real CPU/RAM cost once tables hit millions of rows |
| Migrations | `golang-migrate` (paired `.up.sql`/`.down.sql`) | Hand-applying SQL to a running DB, editing a shipped migration | Migrations are the only supported schema-change path, replayed sequentially on upgrade |
| Task queue / event bus | NATS JetStream | RabbitMQ (default) | Erlang/OTP broker has meaningfully higher idle RAM than one small Go static binary — RabbitMQ stays a documented alternative only |
| Frontend components | shadcn/ui (copied into repo) + Radix + Tailwind | Any other component library as the base, inline hardcoded colors | Keeps the design system ownable/themeable; hardcoded colors break white-labeling |
| API client (frontend) | Generated from OpenAPI (`npm run gen:api`) | Hand-written `fetch` calls with hand-typed shapes | Prevents frontend/backend drift |
| Go↔Go RPC | gRPC + mTLS, `.proto` as source of truth | Ad-hoc REST between Core and Agent | Contract versioning discipline needed across a large, version-skewed agent fleet |

## 2. Absolute rules (never violate)

1. **No hardcoded filesystem paths.** Every path goes through `internal/paths` or `filepath.Join`. Distro differences (Debian vs. RHEL config locations) are data, not code.
2. **`CGO_ENABLED=0`, always.** Cross-compilation to `linux/amd64` and `linux/arm64` must never break.
3. **Linux-only syscall code behind interfaces**, implemented in `_linux.go` build-tagged files. Everything else must compile and unit-test on Windows/macOS.
4. **Never reuse or renumber a shipped `.proto` field.** Only add new optional fields — Core and Agent versions will not always match during rolling upgrades.
5. **Never edit a shipped migration.** Add a new one. Every migration ships as a paired up/down.
6. **No inline/hardcoded UI colors.** Theme via tokens in `web/app/globals.css` only — this is what makes white-labeling a config change, not a refactor. (See [Design.md](Design.md).)
7. **No ad-hoc authorization checks.** All `role ==` / ownership checks go through the centralized RBAC/policy middleware (`internal/auth`). Scattered per-handler checks are exactly where IDOR bugs slip in at this scale.
8. **Every provisioning/suspension/permission-changing action writes an audit-log row** — actor, target resource, timestamp. Not optional, not deferred.
9. **Every agent-directed task must be idempotent and safely retryable.** Some fraction of jobs will always fail transiently at scale — design for re-running, not just success.
10. **Passwords: Argon2id only** for human users. mTLS certs (machine-to-machine) are a separate lifecycle — never conflate the two auth mechanisms.
11. **Secrets never touch plaintext task payloads.** Generated credentials (DB/FTP passwords) go through `internal/secretcrypt` (AES-GCM) in transit; mail passwords are bcrypt-hashed at rest in Core.
12. **License is Apache-2.0.** Don't add dependencies under licenses incompatible with that (copyleft licenses that would force relicensing) without flagging it first.
13. **Every add-on/integration is opt-in, never opt-out.** No silently-bundled commercial-style extras (the cPanel anti-pattern this project explicitly rejects — see `plan.md` Appendix A).

## 3. Error handling

- Wrap errors with context (`fmt.Errorf("...: %w", err)`) — never swallow an error silently, never return a bare `err` without saying what operation failed.
- Agent tasks report failure back through `ReportTaskResult` with enough detail for the operator to act — no silent task death.
- Distinguish **transient** failures (retry, let JetStream redeliver) from **permanent** ones (dead-letter immediately, don't burn 5 retries on a validation error).
- Validate at system boundaries (user input, API request bodies, task payloads) — trust internal code and already-validated data; don't re-validate everywhere defensively.
- Config generation follows validate-then-reload (e.g. `nginx -t` before any reload) — never a blind service restart.

## 4. Testing expectations

- Table-driven Go tests for all platform-neutral logic; must run on any dev OS without Linux or docker-compose.
- Integration tests (real Postgres/Redis/NATS via docker-compose) for handler→store→DB round trips.
- Agent's Linux-only code (systemd, cgroups, PAM) tested in a Linux container/WSL2/CI — never assume it runs on a dev's native OS.
- New endpoints/features aren't "done" without at least one E2E-verified path (see `task.md`'s verification convention).
- Full detail: `.claude/skills/testing-conventions`.

## 5. API/versioning discipline

- All REST routes versioned under `/api/v1/...` from day one.
- OpenAPI spec is **generated** from Go handler annotations — never hand-edited alongside drifted code.
- Regenerate the TypeScript client (`make openapi && cd web && npm run gen:api`) any time the backend API changes — don't let it go stale.

## 6. Version policy

Version numbers anywhere in the docs (Go, Next.js, Postgres, etc.) are **not pins** — verify the current latest stable (or current LTS where published) online at implementation time, and use that. Never deliberately build against an older release when a newer stable one is available, except a documented, inline compatibility blocker.

## 7. What the AI should proactively do

- Update `task.md` and, once a phase's status changes, [Memory.md](Memory.md) — don't let progress docs drift from actual code.
- When a skill's convention and the actual code disagree, fix the skill (or flag the disagreement) rather than silently picking one.
- When extending a config generator or adapter (e.g. adding Apache alongside Nginx), implement it behind the *existing* adapter interface — never a parallel one-off path.
- Keep `plan.md` and `upcoming-features.md` consistent when either changes, per the standing rule in project memory.

## 8. What the AI should never do

- Never add a second, hand-maintained API client alongside the generated one.
- Never build a full modular adapter (Apache, BIND9, ProFTPD, Postgres-for-user-DBs, etc.) speculatively ahead of real demand — MVP ships one default per category; adapters are prioritized post-MVP by actual demand (`upcoming-features.md` §7).
- Never introduce a second message-queue/event mechanism alongside NATS JetStream for something that fits the existing subject model.
- Never commit to git unless the user explicitly asks (per current project convention — no commits made yet by design, waiting on user go-ahead).
- Never run destructive git operations, force-push, or skip hooks without explicit instruction.
