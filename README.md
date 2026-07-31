<p align="center">
  <img src="docs/brand/cypherpanel-lockup.svg" alt="CypherPanel" width="288">
</p>

<p align="center">
  An open-source, resource-efficient alternative to Dokploy/Coolify/cPanel/WHM.
</p>

---

CypherPanel is a hosting control panel for people who run servers for other people — agencies, small hosts, and teams managing a fleet rather than a single box. It gives you the things a hosting panel has to have (accounts, domains, DNS, mail, databases, SSL, backups) with the operational shape modern tooling expects (a real REST API, signed webhooks, a CLI, an audit trail).

**Why another panel?** The established ones are heavy. cPanel's installer refuses to run below 2GB RAM, and its agent footprint scales with what it manages. CypherPanel splits into a central control plane and a deliberately small per-server agent that targets **under 50MB idle RSS** — so the machine's memory goes to the sites it hosts, not to the thing managing them. The newer PaaS-style tools (Dokploy, Coolify) are lighter but assume you are deploying *your own* apps; they have no concept of a reseller, an end-user account, or a mailbox. CypherPanel is built for multi-tenant hosting from the schema up.

> **Status: pre-1.0 and not yet recommended for production.** The feature set below is implemented and builds green, but it has not been run at scale or independently security-audited. See [task.md](task.md) for what is verified and what is still open.

## What it does

**Hosting accounts** — Provision an account and the agent creates the Linux user, nginx vhost, and a dedicated PHP-FPM pool. Suspend, unsuspend, and terminate are first-class. Packages define the limits (disk, file count, bandwidth, domains, databases, mailboxes, CPU, memory), and resellers get their own scoped pool of accounts they can manage without seeing anyone else's.

**Domains & DNS** — A zone editor over PowerDNS with per-record-type validation, and writes that fan out to secondary nameservers so a DNS cluster stays in sync.

**SSL** — Free certificates via Let's Encrypt, including wildcards through DNS-01. Renewal re-runs the same idempotent task as first issuance, so there is no separate renewal path that can rot.

**Email** — Postfix + Dovecot virtual mailboxes with quotas. Deliverability records (MX, SPF, DMARC) are published automatically, and each domain gets a DKIM signing key generated on the mail server — the private half never leaves it.

**Databases & files** — Per-account MariaDB databases and users with a phpMyAdmin/Adminer handoff, Pure-FTPd virtual users, and a file manager that runs as the account's own user. Uploads and archive extraction enforce the package's disk *and* inode quotas, and every archive entry is validated against the account root before anything is written.

**Backups** — Incremental, deduplicated snapshots via restic to local, S3, SFTP, or a restic REST server. Database dumps are captured in the same snapshot as the files, so a restore is actually consistent. Restore lands in a staging directory by default; overwriting live data takes an explicit second confirmation. See [docs/backups.md](docs/backups.md).

**Operations** — A web terminal that opens a shell as the account's user (not root), per-account cron, Prometheus metrics, an append-only audit log with age-based retention, and fleet grouping by region.

**Integration** — Everything the UI can do is a documented REST endpoint. On top of that: signed [webhooks](docs/webhooks.md) with retry and a delivery log, a [plugin manifest](docs/plugin-manifest.md) with a permission model, and `cypherctl` for the terminal.

## How it fits together

```
                    ┌──────────────┐
   browser ────────▶│   CypherUI   │  Next.js — talks only to Core, same-origin
                    └──────┬───────┘
                           │ REST /api/v1
                    ┌──────▼───────┐
                    │  CypherCore  │  Go control plane
                    └──┬────────┬──┘  PostgreSQL · Redis · NATS JetStream
             gRPC/mTLS │        │ tasks + events
                    ┌──▼────────▼──┐
                    │ CypherAgent  │  one per managed server, <50MB idle
                    └──────────────┘  nginx · PHP-FPM · MariaDB · Postfix · PowerDNS
```

Agents **dial out** to Core over mTLS and pull their work from a per-server queue. The panel never initiates a connection to an agent, so managed servers do not need an inbound port open for CypherPanel itself. Work is asynchronous and idempotent: a task may be redelivered, so every handler is written to converge rather than to run exactly once.

```
cmd/core/       CypherCore — REST API, scheduler, agent gRPC endpoint
cmd/agent/      CypherAgent — per-server daemon
cmd/cypherctl/  CLI over the same REST API
internal/       Shared Go packages (config, auth, audit, paths, platform, ...)
proto/          gRPC contract between Core and Agent (source of truth)
migrations/     PostgreSQL schema (golang-migrate)
web/            CypherUI (Next.js)
scripts/        install.sh / uninstall.sh
```

## Install

On a fresh Linux server:

```sh
sh install.sh --dry-run    # see exactly what it would do, change nothing
sh install.sh
```

The installer detects your distro, refuses to silently reconfigure services it did not install (`--take-over` to override), verifies artifacts before installing, and **never reboots for you**. There is a working uninstaller from day one — `sh uninstall.sh`.

## Development (any OS)

The product runs on Linux, but development works from Windows, macOS, or Linux:

```sh
docker compose up -d --wait   # PostgreSQL + Redis + NATS JetStream
make migrate-up               # apply database schema
make build-local              # native binaries into bin/local/
bin/local/cypher-core         # http://localhost:8080/healthz

cd web && npm install && npm run dev   # CypherUI at http://localhost:3000
```

Create the first admin user:

```sh
bin/local/cypher-core create-admin --username admin --password 'choose-a-strong-one'
```

The UI proxies `/api/*` to CypherCore (set `CYPHER_CORE_API_URL` if it is not on localhost:8080), so there is no CORS setup. After changing the backend API, regenerate the spec and the typed client: `make openapi && cd web && npm run gen:api` — the client is generated, never hand-edited.

Configuration is entirely environment-driven; copy [`.env.example`](.env.example) and adjust. Production binaries are always cross-compiled for Linux with `make build` (amd64 + arm64, CGO disabled).

## Documentation

| | |
| --- | --- |
| [plan.md](plan.md) | Full architecture and design rationale |
| [task.md](task.md) | What is built, what is verified, what is open |
| [upcoming-features.md](upcoming-features.md) | Post-MVP roadmap |
| [docs/security.md](docs/security.md) | Security posture and release checklist |
| [docs/backups.md](docs/backups.md) · [docs/webhooks.md](docs/webhooks.md) · [docs/mail-setup.md](docs/mail-setup.md) | Feature guides |
| [docs/upgrade.md](docs/upgrade.md) · [docs/compatibility-matrix.md](docs/compatibility-matrix.md) | Upgrades and Core/Agent version support |

## Contributing

Three rules carry most of the weight:

- **No hardcoded filesystem paths.** Use `filepath.Join` and the `internal/paths` layout layer. Distro differences (Debian vs RHEL config locations) are data, not code.
- **Linux-only syscall code lives behind interfaces** in `internal/platform` with `_linux.go` build tags. Everything else must compile and unit-test on any OS, so contributors are not forced onto Linux to run the test suite.
- **Secrets never travel in task payloads.** JetStream retains messages, so anything in a payload outlives the task. Agents either generate a secret themselves or fetch it over the authenticated gRPC channel.

Shell scripts, templates, and configs are LF-only (enforced via `.gitattributes`).

## License

Apache-2.0 — see [LICENSE](LICENSE).
