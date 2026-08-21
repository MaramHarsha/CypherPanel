# CypherPanel — Glossary

> Canonical vocabulary. UI copy, API resource names, code identifiers, and docs MUST use these terms. When porting from the references, translate their terms using the mapping table at the bottom — the two repos use conflicting vocabulary, and importing it unmapped will poison ours.

## Organizational model

- **Team** — the tenancy boundary. Owns servers, projects, tokens, and billing-free everything. Users belong to teams with a role.
- **Project** — a group of environments for one product/customer. Belongs to a team.
- **Environment** — a named context inside a project (`production`, `staging`, or a preview). Holds resources. Previews are ordinary environments with a TTL.
- **Resource** — anything deployable that lives in an environment: an Application, a Managed Database, or a Compose Stack.

## Resources

- **Application** — a resource built from a git repository or a container image, owned end-to-end by CypherPanel (build → deploy → route).
- **Managed Database** — a database engine instance (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey at v1) provisioned and operated by the panel: lifecycle, credentials, backups.
- **Compose Stack** — a multi-container resource defined by a compose file, either user-supplied or instantiated from a Template.
- **Template** — a declarative one-click definition in `templates/` that instantiates a Compose Stack with generated secrets and domains.

## Infrastructure

- **Server** — a host running cypher-agent. Identified by its agent certificate, not by stored credentials.
- **Control Plane** — the `cypherd` process + PostgreSQL. Holds desired state; runs no user workloads.
- **Agent** — `cypher-agent`; dials home, reconciles, streams. See [ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md).
- **Builder** — an agent running with `--role=builder`; the only place builds execute.
- **Driver** — an orchestrator backend implementation inside the agent (`docker`, `swarm`, later `k8s`). Also used for proxy backends (Traefik, later Caddy).
- **Proxy** — the per-node Traefik instance, configured by the local agent.

## Operations

- **Desired State** — what should be true, stored in Postgres. **Observed State** — what agents report is true. The system's job is making them equal ([ADR-005](adrs/ADR-005-desired-state-reconciliation.md)).
- **Reconciler** — the control loop (in agents and scheduler) that converges observed toward desired. Must be idempotent.
- **Work Item** — an idempotent, keyed command published on `work.*` (build, backup, cert-renew).
- **Build** — producing and pushing an image from a source revision. **Deployment** — a recorded transition of a resource from one desired revision to another.
- **Preview Environment** — an environment created from a pull request, destroyed on merge/close or TTL expiry.
- **Join Token** — single-use, short-lived token that lets a new agent enroll and obtain its mTLS certificate.
- **Backup Target** — an S3-compatible storage destination for backups.
- **Panel Mail** — the panel's own outbound email transport: one SMTP configuration owned by the panel rather than by a project, used for account mail the panel itself must send. Distinct from a **Notifier**, which is a project's channel for telling *people* about *its* events.
- **Email Change** — a pending, single-use, short-lived request to move an account to a new sign-in address. It is only applied once the new address proves it can receive mail.

## Term mapping to the reference repos

| CypherPanel | Coolify | Dokploy |
|---|---|---|
| Team | Team | Organization |
| Project | Project | Project |
| Environment | Environment | Environment |
| Application | Application | Application |
| Compose Stack | Service | Compose |
| Template | Service template (`templates/compose/`) | Template (remote registry) |
| Managed Database | Standalone database (`StandalonePostgresql`, …) | Database (`postgres.ts`, …) |
| Server | Server | Server |
| Backup Target | S3 Storage | **Destination** |
| *(no equivalent — we don't use the term)* | **Destination** (= a Docker network on a server) | — |

## Banned terms

- **"Destination"** — means a Docker network target in Coolify and an S3 backup bucket in Dokploy. Never use it; say **Server**, **network**, or **Backup Target**.
- **"Service"** — means a template stack in Coolify, a Swarm unit in Docker, and a code-layer class in Dokploy. In prose, always qualify; as a resource name, use **Compose Stack**.
