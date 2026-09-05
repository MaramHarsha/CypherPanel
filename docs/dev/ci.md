# CypherPanel — CI/CD Workflow Inventory

> GitHub Actions workflows, listed by **when each file should first exist**. Workflows are created just-in-time like every other artifact — a workflow with nothing real to check is a stub, and stubs are banned ([project-structure.md](../project-structure.md) rule 3).

## Phase 1 — created with the first Go code

### `.github/workflows/ci.yml` — every PR and push to main
- **Go:** `gofmt` check, `golangci-lint`, unit tests for `core/`, `agent/`, `pkg/` with `-race`, build check for linux/amd64 **and** linux/arm64 (cross-compile is cheap; catching ARM breakage in CI beats catching it on a user's Raspberry Pi).
- **Proto:** `buf lint` + `buf breaking` against main — mechanically enforces ENGINEERING.md rule 18 (never break the agent wire protocol).
- **Web:** pnpm install, typecheck, lint, tests, production build. Path-filtered so backend-only PRs skip it.
- **Generated-code drift:** regenerate sqlc, buf, and the OpenAPI client, then `git diff --exit-code`. Enforces "the spec is the source of truth" (ENGINEERING.md rule 19) — hand-edited generated files and forgotten regeneration both fail loudly.

### `.github/workflows/integration.yml`
Dockerized Postgres via `services:`, boots a real `cypherd` and real `cypher-agent`. Jobs:
- **handshake** — enrolls a containerized agent; verifies the mTLS handshake, heartbeat-driven status, a plane-outage reconvergence, and revocation-on-delete. This **is** Phase 1's acceptance test, automated.
- **installer** — the `curl | sh` join under 60 s on a fresh Ubuntu container, that a pasted command with no `CYPHER_AGENT_URL` reaches for the project's latest release asset instead of dead-ending, plus tampered-CA-fingerprint refusal.
- **store-tests** — the store layer against a real Postgres (ENGINEERING rule 29).
- **deploy** — Phase 2 acceptance: a host-run agent (real Docker + git) clones a repo, builds a Dockerfile image, health-gates the rollout, the container actually serves, then a rollback re-ships the revision with the build skipped.
- **deploy-resilience** — Phase 2 acceptance gate 2: a deploy triggered while the agent is down waits in the file-backed WORK stream; on restart the agent drains it and converges to the new revision with no manual step.

Still to grow: real Traefik + Let's Encrypt routing with a zero-dropped-requests check across a live flip (acceptance gate 1), and the two-agent build-relay scenario (ADR-008).

### `.github/dependabot.yml`
Weekly grouped updates: Go modules, pnpm, and Actions versions. Every new runtime dependency still requires PR justification per [tech-stack.md](../tech-stack.md).

## Phase 2 — created with the first release

### `.github/workflows/release.yml` — on version tag
GoReleaser: builds `cypherd` (web UI embedded via `go:embed`) and `cypher-agent` for linux amd64/arm64, checksums, changelog, GitHub Release, multi-arch Docker images to GHCR. **Care point:** these artifacts are also what the agent self-update channel (ADR-010) will serve — artifact naming and versioning chosen here become a compatibility contract.

### `.github/workflows/security.yml` — scheduled + on PR
`govulncheck` (Go CVEs), CodeQL, `gitleaks` (leaked secrets in history), Trivy scan of release images. For a product whose compromise means fleet compromise, this workflow is part of the trust story, not hygiene.

## Phase 4 — created with the catalog and public community

### `.github/workflows/templates.yml`
Path-filtered to `templates/`: schema-validates template YAML, lints compose syntax, verifies referenced images exist. The supply-chain gate for the catalog (malicious-template scenario in the threat model).

### `.github/workflows/docs.yml`
Link-checks the docs tree (our docs are dense with relative links; rot is otherwise inevitable). Later: deploys the user-facing docs site.

### Community hygiene (only once the repo is public)
PR labeler, stale-issue policy, `CODEOWNERS`, issue templates. Before that they're noise.

## Conventions

- **Path-filter aggressively** — both reference repos do; contributors deserve fast feedback.
- **Read Coolify's Actions before writing `release.yml`** — their multi-arch Docker build setup is battle-tested and worth mining (extraction rules in [research/coolify.md](../../research/coolify.md) apply).
- A red main branch blocks all merges; there is no "merge anyway" culture (ENGINEERING.md rule 30).
