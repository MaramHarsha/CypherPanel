# CypherPanel Security Posture

This is the consolidated security model — the controls already in the codebase
plus the hardening checklist for a release candidate. Security is defence in
depth: an application-level check being missed should still leave the OS or a
scoped credential enforcing isolation.

## Authentication & authorization

- **Passwords**: Argon2id (PHC format, OWASP params). Never stored or logged in
  plaintext.
- **Sessions**: 15-minute JWT access tokens with role + scope claims;
  single-use refresh tokens in Redis, rotated on every use; one-shot 401 retry
  in the client. Access tokens are memory-only in the browser.
- **RBAC + resource scoping**: every privileged endpoint gates on role
  (`auth.RequireRole`) **and** scopes the resource to the caller
  (`auth.OwnerFilter` / `auth.CanAct`) — role gating alone is not enough. IDOR
  is prevented centrally; a reseller acting on another owner's resource gets a
  404 (indistinguishable from "missing", so scope can't be probed).

## Secrets

- **DB / FTP passwords** are **generated on the agent** (never in a task payload
  that lands in stream storage), returned as result metadata, and stored
  **AES-256-GCM encrypted** at rest (`internal/secretcrypt`), never in a
  plaintext column. The encryption key is required in production
  (`CYPHER_DB_ENCRYPTION_KEY`).
- Structured logs never include secrets, tokens, passwords, or full payloads.
  Error messages that would echo a password (e.g. `CREATE USER ... IDENTIFIED
  BY`) deliberately omit the statement.

## Transport

- **mTLS** on the Core↔Agent gRPC channel (required in production; a valid
  agent client certificate is the authorization to talk at all).
- **No CORS**: the UI reaches the API same-origin via Next.js rewrites.
- The Prometheus `/metrics` endpoint is unauthenticated by design (scrape
  convention) and **must be network-restricted** by the operator.

## Input validation & injection defence

- **File manager**: every path is `CleanRel`-neutralised then re-verified under
  the account root after symlink resolution; operations run as the account
  uid/gid (`setfsuid`), refusing uid 0. This is the highest-risk surface.
- **Databases**: identifiers are regex-guarded before interpolation into DDL;
  grants are **least-privilege** (a DB user can touch only its own database).
- **DNS**: per-record-type validation at the API boundary; record names must be
  within the account's own zone.
- **Service control / cron / PHP runtime**: service names and actions are
  allowlist-validated at the Core boundary **and** re-checked on the agent;
  versions must match a strict pattern before touching the package manager.

## Isolation

- Each hosting account runs as a dedicated locked Linux user with its own
  PHP-FPM pool + socket; FTP virtual users and cron jobs map to that uid/gid so
  the OS enforces separation.

## Version & supply-chain

- Core refuses agents below `MinAgent` at registration (compatibility matrix).
- CI runs **`govulncheck`** (Go vuln scan), **ShellCheck** (installer scripts),
  and **gitleaks** (secret scan). Keep the Go toolchain on the latest patch —
  stdlib advisories are resolved by rebuilding with current stable Go (CI pins
  `go-version: stable`).

## Release-candidate hardening checklist

- [ ] Rate-limit auth endpoints (login/refresh) to blunt credential stuffing.
- [ ] Security headers on the UI (CSP, HSTS, X-Content-Type-Options).
- [ ] Per-account disk/inode **quota** enforcement in the file manager and
      uploads (size caps exist; quota checks are pending — see the
      `filesystem-operations-safety` skill).
- [ ] Archive/zip-slip guards for uploads/extraction.
- [ ] Restrict `/metrics` exposure (network policy / scrape token).
- [ ] Pen-test the file manager and DNS/DB boundaries before GA.
