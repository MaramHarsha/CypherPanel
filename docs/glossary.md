# CypherPanel — Glossary

> Canonical vocabulary. UI copy, API resource names, code identifiers, and docs MUST use these terms. When porting from the references, translate their terms using the mapping table at the bottom — the two repos use conflicting vocabulary, and importing it unmapped will poison ours.

## Organizational model

- **Team** — the tenancy boundary. Owns servers, projects, tokens, and billing-free everything. Users belong to teams with a role.
- **Invitation** — a single-use, seven-day link that lets one email address join one Team at one role. Only a hash of its secret is stored, it may be revoked, and it grants nothing until it is accepted — at which point the invitee chooses their own password if the panel does not already know the address. The way *into* a team from outside it; adding a member by address is the way in for an account that already exists.
- **Access Request** — a Team member asking that team's owners for a higher role, with a message and a recorded decision. The mirror image of an Invitation: no secret, no expiry, and it grants nothing on its own. What the 403 screen's "Request access" opens.
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
- **Shared Variable** — a value defined once for a whole Project, or narrowed to one Environment of it, and referenced from an Application's environment variables as `{{shared.KEY}}`. Sealed and write-only exactly like an Application's own env vars. Scope is a property of the variable, not of the reference.
- **Webhook Endpoint** — an operator-registered URL that receives **outbound** webhooks: signed JSON POSTed on the observed transitions it subscribes to. The machine-facing twin of a **Notifier**, which tells *people*.
- **Delivery** — one event queued for one Webhook Endpoint, with the exact body bytes that were signed and a record per attempt. Bounded retries; replayable.
- **Endpoint Health** — a Webhook Endpoint's state *derived* from its recent terminal Deliveries (never stored): `healthy`, `failing`, or `unknown` for an endpoint that is disabled or has delivered nothing yet.
- **Inbox** — the panel's own per-user record of what happened to the resources that user can see, counted on the bell in the top bar. Needs no configuration and carries no credential, which is what distinguishes it from a Notifier.
- **DNS Provider** — the panel's connection to a DNS operator (Cloudflare at v1). One per panel, owned by panel admins, its API token sealed. What proves an operator owns a domain is possession of a token that can see its Zone.
- **Zone** — a domain a connected DNS Provider is authoritative for (`example.com`). Cached from the provider, never operator-entered — an operator-entered zone list would be a second place to lie about ownership.
- **DNS Record** — a record CypherPanel created and therefore manages: it is written when a verified domain is set, updated when it changes, and deleted when the Application, Environment or Project goes away. The panel never modifies a record it did not create.
- **Deploy Protection** — desired state about *deploying*: an Environment's declaration of who must approve a deploy there and when deploys are not allowed at all. Enforced once, where a Deployment is born, before any Work Item is published. Off by default, and never applied to a preview environment.
- **Freeze Window** — a weekly recurring interval, declared in its own IANA time zone, during which an Environment refuses deploys. Half-open and allowed to wrap the week (`Fri 18:00 → Mon 08:00`). Evaluated on wall clock in that zone, so it stays put across a DST change.
- **Break Glass** — a bounded, recorded override of an Environment's Freeze Window: a team owner opens one with a required reason and it lapses on its own after 30 minutes. It never bypasses an approval requirement, and it is never revoked early.
- **Audit Event** — one immutable record of a sensitive action: who did it (an **Actor**), what they did (a dotted **action** from a closed vocabulary, e.g. `application.deleted`), which resource it happened to, the ownership chain it belonged to, whether it succeeded, and where the request came from. Every name and id in it is a *snapshot* taken when the action happened, never a live reference — an audit event outlives the resource it describes, which is the whole reason it exists. Distinct from an **Inbox** item, which tells a person that something *happened to their resources*, and from a **Notifier**/**Webhook Endpoint** delivery, which tells someone outside the panel: an Audit Event is the panel's own record of what a *principal did*, kept whether or not anyone is watching.

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
- **"Webhook"** — unqualified it names two opposite directions. Always say **inbound webhook** (a git provider calling us to trigger a deploy, `POST /webhooks/github/{id}`) or **outbound webhook** (us calling an operator's **Webhook Endpoint**). Unqualified, it is as ambiguous as "Service".
