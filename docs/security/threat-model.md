# CypherPanel — Threat Model

> Written at the start of roadmap [Phase 1](../roadmap.md), **before the first line of agent code**, because the agent↔plane trust boundary is the product's central security claim (ADR-002) and it is far cheaper to design against these threats than to retrofit. This document is binding: the security requirements in §8 are acceptance criteria for Phase 1 code, cross-referenced from [ENGINEERING.md](../../ENGINEERING.md) §Security. It is a living document — new attack surface (a new driver, a new provider) adds a scenario here in the same PR.

## 1. Scope and method

We model the system a CypherPanel operator actually runs: one control plane (`cypherd` + PostgreSQL) and N servers each running `cypher-agent`, some with `--role=builder`. We use a lightweight STRIDE lens per trust boundary, but organize by **attacker scenario** because that is how these systems actually fail in the field (evidence: [research/community-pain-points.md](../../research/community-pain-points.md)).

**In scope for this document:** the Phase 1 surface (enrollment, mTLS channel, heartbeat, the control-plane API and its single admin identity) plus the boundaries Phase 2–4 will cross (builds, templates, preview environments), modeled now so the architecture doesn't foreclose defending them.

**Explicitly out of scope:** multi-region / HA control plane (out of scope per [vision.md](../vision.md)); host OS hardening below the agent (the operator's responsibility — we document expectations, we don't enforce kernel policy); physical security; and the security of workloads users deploy *through* us (we isolate them; we don't audit their code).

**The guiding asymmetry (ADR-002):** the reason this architecture exists is that Coolify stores SSH private keys and a panel compromise there yields root on every managed server. Our central requirement is that **a full compromise of the control plane must not yield code execution on connected servers.** Everything below is measured against whether it preserves that property.

## 2. Assets, ranked

What we protect, most to least catastrophic if lost:

| # | Asset | Where it lives | Why it ranks here |
|---|---|---|---|
| A1 | **Ability to execute code on managed servers** | Emergent — from agent trust in the plane | This is the fleet. Coolify's stored SSH keys make this A1 loss a single query; our whole design exists to keep it un-stealable. |
| A2 | **The control-plane signing key / CA** (issues agent client certs) | Control-plane host, encrypted at rest | Whoever holds it can mint an agent identity or impersonate the plane. Root of the mTLS trust. |
| A3 | **Application & database secrets** (env vars, DB credentials, registry creds, provider tokens) | Postgres (encrypted), delivered to the serving node | Direct breach of user data; the reason "mask by default" is ENGINEERING rule 20. |
| A3b | **The DNS Provider token** (Cloudflare, `DNS:Edit`) | Postgres (sealed), used only by the plane | Ranks above the rest of A3 because it acts *outside* the panel: whoever holds it repoints any zone it covers — including MX, which is mail interception, and including the panel's own hostname. It is also what proves domain ownership, so losing it silently un-verifies every domain (§5.12). |
| A4 | **Admin/user authentication material** (password hash, session tokens, API tokens, TOTP seeds) | Postgres | Account takeover → A3, and (bounded, not A1) fleet *command*. |
| A5 | **Join tokens** (in flight, during the enrollment window) | Installer invocation → agent memory → plane | A leaked valid token lets an attacker enroll a rogue agent (see §5.3). Single-use + short-lived by design. |
| A6 | **Desired state & audit history** | Postgres | Integrity matters: silently altering desired state is how an attacker turns the reconciler into their deployment tool. |
| A7 | **Log and metric streams** | `logs.*` / `state.*`, bounded retention | Confidentiality (logs leak secrets if we're careless — rule 20) and availability (the disk-fill killer, §5.9). |

## 3. Trust boundaries

Mapped onto the architecture diagram ([architecture.md §2](../architecture.md#2-system-architecture)). Each numbered boundary is where data changes trust level and therefore must be validated/authenticated.

```
  [ Internet / user ]
        │  TB1: browser ⇄ Core API (public HTTPS, session/token auth)
        ▼
┌─────────────── CONTROL PLANE (trusted core) ───────────────┐
│  Core API ── TB2 ──> PostgreSQL (in-host, but secrets      │
│     │                 encrypted at rest: A2/A3/A4)          │
│     └── embedded NATS ── TB3 (subject-level authz)          │
└───────┬────────────────────────────────────────────────────┘
        │  TB4: agent ⇄ plane — mTLS, outbound-only from agent
        ▼
┌─────────────── SERVER (semi-trusted edge) ─────────────────┐
│  cypher-agent ── TB5 ──> Docker socket (root-equivalent)   │
│     │          ── TB6 ──> Traefik file provider (atomic)   │
│     └── builder ── TB7 ──> arbitrary user build input       │
└─────────────────────────────────────────────────────────────┘
        │  TB8: git provider / registry / cloud APIs (outbound)
        ▼
  [ third-party services ]
```

- **TB1** — the public attack surface. Anyone on the internet reaches it. The Next.js/RSC CVE that turned Dokploy's own dashboard into an attack surface ([community-pain-points.md](../../research/community-pain-points.md) Reddit finding 6) lives here — and is largely designed out by ADR-001 (no server-side web framework; static assets from a Go binary). What remains is our own API code.
- **TB4** — the boundary this product is *about*. Outbound-only from the agent (no inbound ports on servers, ADR-002), mutually authenticated with certificates, never SSH.
- **TB5** — the sharpest edge: the agent holds the Docker socket, which is root-equivalent on the host. This is why agent-compromise blast radius (§5.2) is a first-class scenario and why the plane must never be *able* to hand the agent an arbitrary shell command (§5.1).
- **TB7** — build input is attacker-influenced by definition (it's a git repo, possibly from a fork PR). Modeled in §5.5 and §5.6.

## 4. Threat actors

| Actor | Capability | Primary goal |
|---|---|---|
| **External unauthenticated** | Reaches TB1; scans for the agent's (absent) inbound ports | Find an unauthenticated endpoint; enroll a rogue agent; exploit the panel framework |
| **Malicious/compromised user account** | Valid low-privilege session on a shared instance (P2 agency, P3 platform) | Escalate across teams; read another tenant's secrets; abuse deploy to run code on shared servers |
| **Fork-PR contributor** | Can open a PR that triggers a preview build (P3) | Exfiltrate production secrets via preview env or build (§5.6) |
| **Template author** | Publishes to the catalog (Phase 4) | Ship a compose/template that runs attacker code on install (§5.5) |
| **Network adversary** | On-path between agent and plane, or between plane and third parties | MITM the mTLS channel; replay; downgrade |
| **Compromised control plane** | Full RCE on `cypherd` + DB read | Pivot to the fleet (A1) — **the scenario the architecture must survive** (§5.1) |
| **Compromised single agent** | RCE on one server / its Docker socket | Move laterally to other servers or to the plane (§5.2) |

## 5. Scenarios

Each scenario states the attack, the property that must hold, the controls, and the residual risk. Controls tagged `[Phase 1]` are requirements on the code written now; others are recorded so the design doesn't foreclose them.

### 5.1 Control-plane compromise → fleet takeover (the defining threat)

**Attack.** An attacker gets RCE on `cypherd` or read access to its Postgres (e.g. via a TB1 API vulnerability, a supply-chain compromise, or a stolen backup). In Coolify's model this is game over fleet-wide: the panel DB holds SSH private keys, so the attacker `ssh`es as root into every server.

**Property that must hold.** Compromising the control plane must **not** grant arbitrary code execution on managed servers. The blast radius is bounded to *what the desired-state model can express* — the attacker can command deployments (which run *their* chosen images — serious, but constrained, observable, auditable, and reversible), but cannot get a raw shell on a server, because no component accepts "run this arbitrary command" over the wire.

**Controls.**
- **No stored server credentials of any kind** (ADR-002) — there are no SSH keys, passwords, or sudo tokens in the DB to steal. `[Phase 1]`
- **The wire protocol has no "exec arbitrary command" verb.** Agents consume *desired state* and reconcile it (ADR-005); the gRPC/NATS surface is deployment intents, not a command shell. The interactive terminal (V1.x) is the one exception and is gated separately (§5.7). This is an ENGINEERING-level constraint on proto design: **`work.*` messages describe state, never shell strings.** `[Phase 1: proto review gate]`
- **Secrets encrypted at rest** with a key held **outside the panel's own auto-update blast radius** (a lesson taken directly from Coolify discussion #3687, where the updater destroyed `.env` including encryption keys). `[Phase 1: config/secrets design]`
- **The CA/signing key (A2) is the highest-value secret** and is stored encrypted, never logged (rule 20), and its compromise is the one path that *does* approach A1 — so it is isolated and, post-v1, a candidate for external KMS/HSM.
- **Full audit log** (V1.x) of every desired-state mutation, so a compromise is reconstructable and the reconciler's actions are attributable.

**Residual risk.** An attacker with a compromised plane *can* deploy malicious images to servers and read A3 secrets from the DB — this is real and serious. What they cannot do is get a root shell fleet-wide from a single stolen key. We reduce the residual further with 2FA (rule/matrix V1), audit, and encryption, and we are honest in [vision.md](../vision.md) that this is the bound, not zero.

Deploy protection ([deploy-protection.md](../features/deploy-protection.md), V1.x) is deliberately **not** claimed as a control here. Its gate is enforced by the plane, so an attacker holding the plane can bypass it exactly as they can command any other deployment. What it reduces is the *accidental* blast radius — the Friday push, the mis-clicked rollback, the CI token that deploys unattended — and the one place it touches this scenario is §5.8's residual: an approval decision — and a change to the policy itself — needs an interactive session, so a stolen API token alone can neither release a deploy nor remove the gate that parked it.

### 5.2 Single-agent compromise → lateral movement

**Attack.** An attacker gets RCE on one server (a vulnerable deployed app escapes its container, or the host was already dirty). The agent there holds the Docker socket (TB5, root-equivalent) and a valid mTLS identity (A-tier trust on TB4).

**Property that must hold.** A compromised agent can harm *its own* server (it already could — it holds that host's Docker socket) but must have **minimal leverage over other servers or the control plane.**

**Controls.**
- **Agent identity is scoped to its own server.** The plane authorizes `state.*`/`work.*` per-agent; one agent's certificate does not let it subscribe to another server's work or publish another server's state. NATS subject authorization is per-identity. `[Phase 1: bus authz]`
- **Reply inboxes are per-identity too.** Request/reply on core NATS answers on the requester's reply subject, and the desired-state sync's answer carries the resolved `DesiredState` — plaintext `env` for every app and database on that server. Core NATS delivers a publication to *every* matching subscription, so a shared `_INBOX.>` Subscribe grant meant any agent could read any other agent's secrets: exactly the lateral movement this scenario forbids. Each agent now connects with `nats.CustomInboxPrefix("_INBOX_<cn>")` and is granted `_INBOX_<cn>.>` and nothing else, so a sync reply reaches its requester alone. **Compatibility:** an agent built before this change keeps the default `_INBOX` prefix, which is no longer granted, so its first request/reply (the JetStream work-consumer bind) times out and it converges nothing until it is upgraded — a deliberate break, because it *is* the fix. Operators are told to upgrade agents in the same window in [deployment.md §Upgrades](../dev/deployment.md#upgrades). `[Phase 4: control-plane-hardening.md §1; proven by core/bus TestAgentInboxIsIsolatedPerIdentity]`
- **The plane validates the reply subject before it answers.** The grant above says who may *subscribe*; it does not say where the plane may *publish*. The requester picks `msg.Reply`, and NATS does not permission-check a publisher's reply subject under plain publish/subscribe allow lists (only `Responses` permissions do — verified against nats-server v2.14.5). The plane's own client is in-process and **unrestricted**, so an unvalidated `msg.Respond` was an arbitrary-subject publish primitive: any enrolled agent could request a sync on its own (permission-pinned) subject while naming `work.<other-server>.rollout`, `state.*.deploy` or `logs.*.>` as the reply, and the plane would publish there from its trusted connection. Not a cross-agent read — the payload is keyed off the request subject, so it is always the requester's own state — but injection into plane-internal subjects. `RespondDesiredState` now requires the reply to sit under `_INBOX_<cn>.` and otherwise drops the request and logs at warn with the server id (once per server: it also names an agent that is older than the plane). `[Phase 4: control-plane-hardening.md §1; proven by core/bus TestSyncReplySubjectMustBeInTheRequestersInbox]`
- **The plane trusts observed state as a *report*, not a command.** A lying agent can misreport *its own* status (causing the scheduler to redeploy, at worst) but cannot mutate desired state or another server's reconciliation. Desired state is written only by the Core API behind user auth (ADR-005). `[Phase 1]`
- **Certificate revocation** — a burned agent's cert can be revoked at the plane, cutting it off; enrollment of its replacement is a fresh join token. Revocation now also denies **renewal** (below), so a revoked identity ages out instead of living to its `NotAfter`. `[Phase 1: mint short-lived certs; revocation list design]`
- **Short-lived agent certificates with rotation** (renew over the authenticated channel) bound the window a stolen cert is useful. `EnrollmentService.Renew` re-signs an agent's certificate over the mTLS channel it already holds: authorization is the verified peer CommonName (a `server_id` in the body is a claim that must agree with it, so no agent can renew another's identity), and the plane refuses an identity it no longer recognises. The agent renews at **two thirds of the certificate's lifetime** with a **fresh key pair** each time — so a leaked key stops being useful when the certificate it belongs to expires, not merely the certificate — and swaps the material atomically (`identity.json` names the pair in use; renaming it is the commit). **TTL decision: 90 days**, Let's Encrypt's number for the same reasons — long enough that a renewal outage is not an emergency, short enough that a stolen certificate is not a permanent grant. The renewal point is a fraction of the lifetime, so shortening `CYPHERD_AGENT_CERT_TTL` shortens the retry window with it rather than making renewal impossible. `[Phase 4: agent-identity-and-tls.md §3; proven by core/enroll, core/api/grpc and agent/identity tests]`
- The agent runs with the least privilege its job needs; where the Docker socket is required it is the documented, understood grant, not an accident.

**Residual risk.** The compromised host itself is forfeit — we cannot prevent that from inside it. We contain lateral movement and make it observable.

### 5.3 Join-token leak / rogue enrollment

**Attack.** The single-use join token (A5) leaks — it's in a `curl | sh` command that might land in shell history, a CI log, a Slack paste, or a screen-share. An attacker uses it to enroll *their* machine as a trusted agent, or races the legitimate install.

**Property that must hold.** A leaked token has a **small, bounded blast radius** and a leaked *used* token is worthless.

**Controls.**
- **Single-use** (ENGINEERING rule 22): the token is consumed on first successful enrollment; a race means one of the two fails and the operator sees an unexpected/duplicate enrollment. `[Phase 1]`
- **Short-lived** (rule 22): tokens expire quickly (minutes, not days); an old token in shell history is inert. `[Phase 1: TTL]`
- **Bound to intent where possible** — a token is issued for a specific pending server registration, so an enrolled agent lands as a known, named server the operator is expecting, and an unexpected enrollment is visible.
- **Enrollment is observable**: a new server appearing is a first-class, audited event surfaced in the UI, so rogue enrollment isn't silent.
- **Constant-time token comparison** (rule 21) — no timing oracle on the token check. `[Phase 1]`
- A rogue agent that *does* enroll is still just an agent: it controls only its own (attacker-owned) host and is subject to every §5.2 bound. It does not gain access to other servers or secrets not scheduled to it.

**Residual risk.** Within the short validity window, a leaked unused token enrolls one rogue (attacker-controlled) server. The operator sees it; it has no cross-server reach. Acceptable given single-use + short TTL + observability.

### 5.4 Attacks on the agent↔plane channel (TB4)

**Attack.** A network adversary tries to MITM, replay, downgrade, or eavesdrop the agent↔plane traffic.

**Property that must hold.** All agent↔plane traffic is confidential, integrity-protected, and mutually authenticated. There is **no plaintext internal traffic** (ENGINEERING rule 23).

**Controls.**
- **mTLS on every connection** (ADR-002, rule 23): both sides present certificates; the agent verifies it's talking to *our* plane (pinned CA), the plane verifies the agent's client cert. Defeats impersonation in both directions. `[Phase 1]`
- **Modern TLS only** (TLS 1.3, strong cipher suites); no downgrade path. `[Phase 1: tls.Config]`
- **Outbound-only from the agent** (ADR-002): servers open no inbound ports, shrinking the external attack surface to zero listening services on the edge. `[Phase 1]`
- **At-least-once + idempotency** (ADR-003, rule 12): replay of a `work.*` message is safe by construction because every consumer is idempotent — a replayed deploy converges to the same state. Replay is therefore not a security event, it's a designed-for normal case.

**Residual risk.** Traffic analysis (an on-path observer learns *that* an agent is communicating and rough volume). Not defended and not worth defending at this tier.

### 5.5 Malicious template (supply chain, Phase 4 surface, modeled now)

**Attack.** A template author (or a compromised upstream template repo) ships a compose/template that, on instantiation, runs attacker code: a backdoored image, a `command:` that exfiltrates env, a bind-mount of the host root, a privileged container.

**Property that must hold.** Instantiating a template is **not** arbitrary code execution on the operator's terms without the operator's understanding. Templates are data, reviewed and constrained — not trusted scripts.

**Controls (design constraints recorded now so Phase 4 can meet them).**
- **Templates are declarative data, never code** ([project-structure.md](../project-structure.md): `templates/` is YAML). No template carries an executable install script the panel runs.
- **The template CI gate** (`templates.yml`, [dev/ci.md](../dev/ci.md)) schema-validates YAML, lints compose, and verifies referenced images exist — the supply-chain checkpoint before a template can enter the catalog.
- **Dangerous compose constructs are surfaced, not silently honored**: `privileged: true`, host bind mounts, host network mode, and `cap_add` are flagged for explicit operator consent (this dovetails with the "advanced Docker passthrough" escape-hatch row — power is opt-in and visible, never default and hidden).
- **Generated secrets, not embedded ones** — the magic-env convention we port from Coolify (`SERVICE_PASSWORD_*`, generated per instantiation — [research/coolify.md](../../research/coolify.md) lesson 4) means a template never ships a real credential; the platform generates them, so a template can't smuggle a backdoor account.
- **Pin/verify image digests** where the catalog can, so a template's referenced image can't be swapped underneath it.

**Residual risk.** A determined author can still publish a template pointing at a legitimately-hosted malicious image. Catalog curation and digest pinning reduce it; operator consent for privileged constructs bounds it. Full defense is a Phase 4 design item, tracked to this scenario.

### 5.6 Fork-PR preview environment secret exfiltration

**Attack.** The classic CI/CD betrayal (P3 platform-engineer surface): an outside contributor opens a pull request whose build or preview environment is granted production secrets. The PR's code — attacker-controlled — reads the secrets and exfiltrates them (prints to build log, phones home).

**Property that must hold.** A preview environment built from **untrusted (fork) code never receives production secrets**, and preview scope cannot reach production resources.

**Controls (Phase 3 surface — preview environments — designed against now).**
- **Preview environments are ordinary environments with their own scope** ([glossary.md](../glossary.md): previews are first-class environments, not a bolt-on). Secrets are environment-scoped; a preview environment gets *preview* secrets, never production's, by the same mechanism that separates staging from production.
- **Untrusted-source policy**: builds triggered by fork PRs (as opposed to branches by trusted collaborators) run without access to protected secrets and with a distinct, lower-trust secret scope — an explicit policy decision surfaced to the operator, not a default that leaks.
- **Build isolation** (§5.7): the build runs on a builder-role agent, not the plane, and its log stream is subject to secret masking (rule 20) so even injected secrets don't trivially print.
- **TTL auto-destroy** (glossary): previews expire, bounding the window of any exposure.

**Residual risk.** If an operator *deliberately* grants a preview environment production secrets (overriding the default), the exfil is possible — that is an informed operator choice, and the default protects them. Recorded so the preview-environment feature spec (Phase 3) must implement the safe default, not just offer the safe option.

### 5.7 Build-time and interactive-exec threats (TB7, TB5)

**Attack.** Build input is attacker-influenced (arbitrary Dockerfile, arbitrary source). A malicious build tries to escape the builder, poison the build cache, consume unbounded resources (§5.9), or read other tenants' build context. Separately, the interactive terminal (V1.x) is, by design, arbitrary command execution on a server — the one sanctioned exception to §5.1's "no exec verb."

**Property that must hold.** Builds are isolated from the control plane (always) and from each other; interactive exec is authenticated, authorized, fully audited, and never a backdoor around desired state.

**Controls.**
- **Builds never run on the control plane** (vision non-negotiable 5; ADR-001) — a build escape lands on a builder-role server, not on `cypherd`. `[architectural, enforced from Phase 2]`
- **Builds run on builder-role agents** with resource caps (the matrix's "build resource caps" row) so a heavy or hostile build can't starve its host — this also fixes the Reddit-reported "Next.js build crashes the VPS" pain.
- **Interactive terminal is a gRPC stream through the agent** (ADR-002), gated behind explicit user authorization, scoped to a resource the user may access, and **written to the audit log** — it is a visible, attributable, revocable capability, not a hidden channel. Because it is the single exception to §5.1, it carries its own feature-spec security section when built (V1.x).

**Residual risk.** Build-cache poisoning across builds on a shared builder is a real concern requiring per-tenant cache scoping in the builder design (Phase 2). Recorded to that scenario.

### 5.8 Web/API attack surface (TB1)

**Attack.** External unauthenticated or low-privilege users attack the public API: authn bypass, broken object-level authorization (reading another team's resources — the classic multi-tenant IDOR), injection, framework CVEs (the Dokploy/Next.js RSC event).

**Property that must hold.** Authentication is enforced before any resource access; authorization is checked per-object against the caller's team/role; there is minimal framework attack surface.

**Controls.**
- **No server-side web framework to CVE.** The UI is static assets served from the Go binary (ADR-001); the RSC/SSR vulnerability class that hit Dokploy structurally doesn't apply (community-pain-points finding 6, an "unplanned security validation of ADR-001"). Our remaining TB1 surface is our own API handler code — smaller and fully ours to audit. `[Phase 1: static asset serving]`
- **Auth on every endpoint by default**; the framework denies unauthenticated access unless a route is explicitly public (login, agent enrollment). `[Phase 1: middleware]`
- **Object-level authorization** — every resource read/write checks team ownership and role, not just authentication. This is the multi-tenant isolation P2/P3 depend on. `[Phase 1 for the admin/server surface; enforced per-resource as resources land]`
- **Login rate limiting and session management** (matrix V1, threat-model deliverable): brute-force protection, lockout, session revocation. `[Phase 1: admin login]`
- **Throttling in two dimensions, keyed by an address the client cannot choose.** Sign-in is counted per client address *and* per account, so one attacker behind a shared proxy cannot lock every account out and a guess spread across a botnet is still bounded by the account's own budget. The client address is the TCP peer unless the peer is inside `CYPHERD_TRUSTED_PROXIES` (a CIDR list, **empty by default**), and then it is the right-most `X-Forwarded-For` hop that is not itself a trusted proxy — so prepending addresses buys nothing. The same limiter guards the email-change request and confirm routes (§5.10). A `429` says how long to wait (`Retry-After` and `retry_after_seconds`), which is honesty, not a hint: the window is fixed and public. `[Phase 4: control-plane-hardening.md §5]`
- **Correlation ids the client cannot forge.** Every response carries `X-Request-Id`, repeated as `trace_id` in every error body, and every 5xx is logged at error level with it. An inbound `X-Request-Id` is honoured only from a trusted proxy and only when it is a short printable token, so a client can neither file its requests under someone else's id nor inject a newline into a log line. A handler panic becomes the same envelope with `500` rather than a dropped connection. `[Phase 4: control-plane-hardening.md §2]`
- **Expired sessions are deleted, not merely ignored.** An hourly sweep removes rows past `expires_at`; an unbounded `sessions` table was a slow disk-growth path (§5.9) and a larger pool of hashes for an attacker who reaches the database. `[Phase 4: control-plane-hardening.md §7]`
- **The panel's own log tail is owner-and-session-only.** `GET /api/v1/panel/logs` names hosts, resources and users, so an API token — which lives in CI — may never read it, and it holds no secrets by construction (rule 20, asserted by a test that seals a value through the API and greps the tail). `[Phase 4: control-plane-hardening.md §4]`
- **A deploy gate an API token can neither open nor remove.** Where deploy protection is enabled, a deploy into the protected environment parks and someone at or above the environment's approver rank must release it — and `POST /deployments/{id}/approve`, `/reject`, `/environments/{id}/break-glass` **and `PUT /environments/{id}/protection`** are `sessionOnly`, so a leaked CI token can neither approve the deploy it just requested nor switch the policy off and deploy freely. The `PUT` is on that list for the stronger reason: deleting the control (`require_approval: false`, `freeze_enabled: false`, `windows: []`) is more powerful than the single 30-minute grant that suspends one freeze, so a gate that stopped at the decision routes would have left the wider door open. Without all four the gate would be decorative: a token inherits its owner's role, so the same reasoning that keeps credential management off tokens applies here. The deploy routes themselves are unchanged, so CI keeps working — its deploys simply wait for a person. `[V1.x: deploy-protection.md §5]`
- **2FA / TOTP** (matrix V1): because panel account takeover is fleet *command* (bounded by §5.1 but still serious), strong account security is not optional. `[Phase 1 admin account supports it; enforcement per matrix]`
- **Secrets masked in all API responses** (rule 20, Coolify's `ApiSensitiveData` idea) — the API never returns a secret it doesn't have to, and masks by default. `[Phase 1]`
- **Constant-time comparison** of tokens and secrets (rule 21). `[Phase 1]`
- **Standard input validation / parameterized queries** — sqlc gives us parameterized SQL by construction (no string-built queries), closing SQL injection structurally.

**Residual risk.** Our own API bugs. Mitigated by the `security.yml` CI (govulncheck, CodeQL, gitleaks — [dev/ci.md](../dev/ci.md), Phase 2) and `SECURITY.md` disclosure process (shipped with going-public, ADR-009 timing).

### 5.9 Availability: silent disk exhaustion & self-DoS

**Attack (mostly self-inflicted, but a real DoS vector).** Build cache, orphaned images, and unbounded logs/metrics fill the disk until the panel's own database can't write and the whole system crash-loops — the **#1 production killer for both references** (community-pain-points Reddit finding 1: `coolify` + `coolify-db` in a crash loop because Postgres couldn't write its lock file). An attacker can accelerate it (spam deploys, log floods).

**Property that must hold.** The control plane protects its own ability to function; resource exhaustion degrades gracefully with warning, never silently bricks.

**Controls.**
- **Desired-state GC** (matrix V1): the agent knows exactly which images/containers/networks/volumes desired state references, so pruning is *principled* (delete what nothing references) rather than the heuristic cleanup that still leaves both references crashing. `[design premise of the reconciler, Phase 2]`
- **The control plane reserves disk headroom for its own database** and self-protects — it will refuse or defer work before it starves its own Postgres. `[Phase 1: the plane's own resource guard is a boot-time concern]`
- **Bounded retention on `logs.*`/`state.*`** (ADR-003 says "bounded retention"; the Dokploy footprint measurement showed 22.76 GiB *written* on an idle install — [research/dokploy.md](../../research/dokploy.md)): JetStream retention limits and sampled/batched metrics cap write churn and disk use by design, not by cleanup-after. `[Phase 1: JetStream stream config sets limits from day one]`
- **Threshold alerts before failure** (matrix V1, upgraded from V1.x precisely because of this finding): the operator is warned while there's still room to act.
- **Every expansion is bounded, in the count *and* the result.** Anywhere the plane substitutes operator text into operator text, the amplification factor is capped, not just the input: a **Shared Variable** reference is bounded at 16 *occurrences* per value (not 16 distinct keys, which left the repeat count free) and the expanded result is capped besides. This is memory, not disk, and it is the sharper version of this scenario — `Scheduler.resolveEnv` is re-entered from `DesiredStateFor` on every agent reconnect, so an unbounded expansion does not fail one deploy, it OOMs the plane and OOMs it again on restart. `[shared-variables.md §6]`

**Residual risk.** A determined authenticated attacker can still generate load; rate limiting and per-team quotas (post-v1) bound it. The self-inflicted case — the common one — is designed out.

### 5.10 Mailbox-as-identity: account takeover through an email change

**Attack.** The panel gains an outbound mail transport (**Panel Mail**) and the
ability to move an account to a new sign-in address ([panel-mail.md](../features/panel-mail.md)).
That introduces a trust the model did not previously contain: the operator's
mailbox. Three ways it bites. **(a)** An attacker who has read an operator's
mailbox — a far softer target than the panel — follows a confirmation link and
inherits the account, which is A4 and therefore fleet *command*. **(b)** An
attacker holding only a live session (a stolen laptop, an unlocked browser)
moves the address to one they control and locks the owner out permanently.
**(c)** The panel's SMTP credentials become a new asset: they are a sending
identity, useful for phishing the operator's own team from a trusted address.

**Property that must hold.** No single stolen thing moves an account. A mailbox
alone, a session alone, or a password alone must each be insufficient — and the
rightful owner is always told, on the channel they still control.

**Controls.**
- **Two factors for the move, not one.** The request needs a live session *and*
  the current password; the confirmation needs a secret that only ever went to
  the new address *and* a live session. (a) and (b) both fail. `[panel-mail.md §4]`
- **The old address is always notified**, naming the new one, at request time.
  This is the only signal left if the session and the password are already lost,
  and it is what turns a silent takeover into a detected one. `[panel-mail.md §5]`
- **Other sessions are revoked when the address changes**, on the same reasoning
  as a password change: the address that can recover the account has moved.
- **The token is single-use, 30 minutes, and hashed at rest**, spent by an atomic
  `UPDATE … WHERE consumed_at IS NULL AND expires_at > now()`, with the secret
  compared in constant time *before* the consume so a wrong guess cannot burn a
  valid change — the `join_tokens` discipline (§5.3) applied unchanged.
- **Both routes are `sessionOnly` and rate limited.** An API token can never
  reach them (§5.8's rule), and the confirm endpoint — a guessing surface — is
  throttled like `Login`.
- **SMTP credentials are sealed** with the master key like every other secret,
  never returned by any route, and absent from errors and logs (§6). A panel
  compromise already implies them; nothing weaker does.
- **The confirmation link is built from the panel's own advertised base URL**,
  never from a request header, so it cannot be pointed at an attacker's host.

**Residual risk.** An attacker holding *both* a live session and the current
password can still move the address — but that attacker has already won the
account by §5.8's measure, and the notice to the old address makes it loud
rather than silent. A compromised mailbox still receives the notice intended to
warn about it; nothing here defends an operator whose mail *and* panel are both
in someone else's hands. Password reset by email is deliberately **not** added
(panel-mail.md §8): it would make the mailbox sufficient on its own, which is
exactly the property this scenario exists to preserve.

### 5.11 The panel as an HTTP client: outbound webhooks

**Attack.** Until now the control plane made no outbound HTTP request to an
address an operator chose, except a notifier's fixed provider host. An
**Outbound Webhook** ([outbound-webhooks.md](../features/outbound-webhooks.md))
makes the panel POST signed JSON to *any* URL a project member registers, on
every subscribed transition, with retries. Four ways that bites. **(a)** The URL
is an egress path out of the control-plane network: `http://127.0.0.1:…`,
`http://10.0.0.7:5432`, `http://169.254.169.254/latest/meta-data/` all resolve
from where `cypherd` runs, not from where the operator sits. **(b)** Unlike a
notifier, every attempt's **status and duration are persisted and readable** via
`GET /webhook-endpoints/{id}/deliveries`, which turns (a) from blind SSRF into a
semi-blind scanner a member can drive on demand with `POST …/ping`. **(c)** The
signing secret is a new asset: whoever holds it can forge events *to the
receiver*, and receivers act on them. **(d)** The payload leaves the trust
boundary — anything put in it is disclosed to whoever controls that URL.

**Property that must hold.** An outbound webhook may carry only what its
subscriber is already entitled to read, must be attributable to us, and must not
become a general-purpose network probe or an unbounded amplifier.

**Controls.**
- **The payload carries no sealed material** — deploy and backup metadata that
  the API already returns to the same caller. Never env vars, connection strings,
  or anything from a `*_ct` column. `[outbound-webhooks.md §6]`
- **Signed, and bound to a moment.** HMAC-SHA256 over `timestamp + "." + rawBody`
  with a fresh timestamp per attempt, `sha256=<hex>` matching this repo's
  existing inbound convention so receivers can reuse a known recipe. The MAC
  covers the raw bytes, and the published contract tells receivers to verify
  before parsing and to dedupe on `X-CypherPanel-Delivery`. `[§4]`
- **The secret is sealed** with the master key, unsealed only to sign, absent
  from the endpoint DTO by construction, and returned exactly twice — at create
  and at rotate. A database read yields ciphertext. `[§6, rule 20]`
- **Redirects are never followed**, so a receiver cannot bounce a signed body to
  a third party, and **response bodies are never stored or logged** — a
  receiver's error page can carry its own secrets. Transport errors go through
  `redactURL` so a token-bearing URL cannot ride out in a `*url.Error`. `[§6]`
- **Authorized at the project, like a notifier.** Every route resolves through
  `projectIDForWebhookEndpoint` / `projectIDForWebhookDelivery` at `RoleMember`;
  non-member 404, under-ranked 403 (§5.8's rule). A delivery is not addressable
  without its endpoint.
- **Bounded** (§5.9's discipline): four attempts, a ~31-minute horizon, a 10s
  per-attempt timeout, and the 200 most recent deliveries per endpoint retained,
  attempts cascading.

**Email has no unsaved test.** Testing a webhook config POSTs a fixed JSON body
to one URL; testing an email config would make the panel relay a message through
an arbitrary SMTP server, with an arbitrary `from`, to an arbitrary recipient,
on a member's say-so and leaving no row behind — spam and spoofing
infrastructure rather than a connectivity check. The email channel is tested
through its saved notifier, which is authorized, recorded and revocable.
`[notify.ErrTestRequiresSave]`

**Testing an unsaved configuration is filtered.** `POST
/projects/{id}/notifiers/test` (notifications.md §6) hands the sender a config
straight from the request body. Three things separate it from a saved notifier:
nothing is persisted, so no row records that the probe happened; the result is
returned **synchronously**, so "connection refused" versus a timeout separates a
closed port from a filtered one; and it can be repeated at will with a different
address each time. That is a port scanner with no trace, so this one path takes
the control named at the end of this section: a destination check resolved **at
request time**, in the dialer's `Control` hook, refusing loopback, RFC1918,
IPv6 unique-local, link-local (cloud metadata), unspecified and multicast
addresses, and their IPv4-mapped forms. Before anything is dialled the URL must
also be `https` with a dotted hostname — no cleartext, no userinfo, no IP
literal, the shape that skips DNS entirely. Because the hook sees the address the
socket is about to use, a name that resolves publicly on the first lookup and
privately on the second is refused on the second. `[core/notify/egress.go]`

**Addresses are parsed, not merely non-empty.** A notifier's `from` and every
recipient in `to` must parse as an address before the config can be stored, so a
line break cannot reach a header in the first place; `buildMessage` still
sanitises on the way out. `[notify.validateConfig]`

**Accepted risk — egress from a *saved* notifier is not filtered.** We enforce
`http`/`https` only and refuse redirects, but for a notifier that has been
stored we do **not** block private, loopback or link-local destinations. This is the same posture as
[notifications.md](../features/notifications.md) §6 and rests on the same
premise: the operator is the trust root and already runs arbitrary containers on
these servers. It is recorded here rather than left implicit because (b) makes
this surface *more* informative than a notifier's — a member can read back
whether a port answered and how fast. Two things make that acceptable today: the
caller must already hold `RoleMember` on a project, and a panel that runs
untrusted members is outside §7's assumptions. **If per-team quotas or untrusted
members ever land, this scenario is the one to revisit** — the control then is a
destination denylist resolved at request time, not at validation time, to avoid
a DNS-rebinding gap.

### 5.12 DNS control: the token that proves ownership

**Attack.** The panel gains a **DNS Provider** — one Cloudflare token with
`DNS:Edit`, used both to prove an operator owns a domain and to write the
records that make it resolve ([dns-automation.md](../features/dns-automation.md)).
Three ways it bites. **(a)** The token is A3b: whoever reads it repoints any
zone it covers. That is not confined to CypherPanel's own records — MX included,
which is mail interception, and the panel's own hostname included, which is
where sessions are issued. **(b)** The panel is now a *writer* in someone's DNS,
so a bug that deletes the wrong record is an outage the operator cannot
attribute to us without reading Cloudflare's audit log. **(c)** Because the
connection is panel-wide (§1 of that spec), any project member who can set a
domain causes a record to be written in the operator's zones under a name they
choose.

**Property that must hold.** The panel writes only records it created, only
inside zones the token already covers, only with content it derived itself —
and losing the token degrades to "nothing is verified", never to "everything is
verified".

**Controls.**
- **The token is sealed** with the master key, unsealed only to call Cloudflare,
  never returned by any route, never logged, absent from error strings (§6,
  rule 20). `PUT` replaces wholesale; there is no partial-secret merge, so a
  masked round-trip cannot be replayed back into storage. `[dns-automation.md §3.1]`
- **We only ever touch records we created.** A record with no `dns_records` row
  is never modified or deleted. Adoption on conflict is narrow — same zone,
  name, type *and* content — so an operator's hand-made record with different
  content is a named conflict, never a silent overwrite. `[§4.4]`
- **Content is derived, never supplied.** The record's value is the app's own
  server address; the only operator-controlled input is a hostname that must
  already fall inside a connected zone. This is what stops (c) from becoming
  "point any name in your zone at any address".
- **Verification is derived, never stored.** `domain_verified` is recomputed
  from the current zone list on every read. A stored flag would survive the
  token being revoked or a zone being removed — a stale *security* decision,
  which is worse than a recomputed one. Revoking the token unverifies
  everything, which fails closed. `[§4.1]`
- **Panel-admin gated.** Every `/panel/dns` route takes `requirePanelRole(admin)`;
  a project member sees only whether their own application's domain is verified,
  never the token, the zone list, or another project's records. `[§5]`
- **Disconnecting deletes nothing.** Removing the provider removes our ability
  to act, not our obligation to be careful: records are left exactly as they
  are. Nothing about losing a credential should destroy an operator's DNS. `[§4.5]`
- **The destination is a constant, and now structurally so.** Unlike §5.11 the
  host is not operator-supplied. The first implementation built request URLs by
  concatenating escaped values onto a base string; CodeQL flagged it
  (`go/request-forgery`) and was right to, because escaping was never the
  control. `URL.JoinPath` **cleans** the path it builds, so a segment of
  `../../..` is not neutralised — it is *resolved*, and an operator-supplied
  account id could redirect the call to a different Cloudflare endpoint with
  this client's bearer token attached. Three things now hold instead: the base
  is a parsed `*url.URL` so scheme and host are structure rather than string;
  every interpolated path segment must be an identifier (no separators, no
  dot-segments) or the request is refused; and a final check fails closed if a
  built URL ever leaves the pinned host. Query values need none of this —
  `Values.Encode` escapes them and they cannot change the path.

**Residual risk.** A token scoped more broadly than CypherPanel's zones can do
more than CypherPanel needs; we tell the operator to scope it and we cannot
enforce that, because a token's scope is Cloudflare's to police. And the
panel-wide choice means (c) is real: a member who can name a domain can create a
record in your zone. It is bounded — inside your zones only, with content they
do not choose, visible and attributable in the UI — but an operator running
untrusted members should scope the token to a zone they do not mind sharing.
**Per-team providers are the control if that assumption ever stops holding.**

### 5.13 The panel as an HTTP client: the update check

**Attack.** The plane polls a release feed (`CYPHERD_UPDATE_FEED_URL`, GitHub's
`releases/latest` by default) every six hours to tell owners a newer version
exists ([control-plane-hardening.md §3](../features/control-plane-hardening.md)).
That is an outbound request the plane makes on its own schedule, to a host it
does not control, and the answer becomes text an owner reads in their inbox.
Three ways it bites. **(a)** A hostile or hijacked feed can redirect the request
inward — `http://169.254.169.254/`, `http://127.0.0.1:5432/` — turning the plane
into an SSRF probe of its own network from inside the trust boundary. **(b)** It
can answer with an unbounded body and make the plane eat memory. **(c)** The
strings it returns (a tag, a notes URL) reach an inbox item, so an unvalidated
one is content injection into an operator-facing surface.

**Property that must hold.** The check is optional, bounded, and can never be
steered into the plane's own network or made to consume it. Nothing it returns
is trusted beyond being displayed as text.

**Controls.**
- **Opt out entirely.** `CYPHERD_UPDATE_CHECK=off` makes no outbound request at all — for an air-gapped install this is the whole answer. A `dev` build also performs no request: there is nothing to compare against.
- **No redirect into private space.** `http`/`https` only, at most three redirects, and a redirect is refused when its target is loopback, private, link-local, unspecified or multicast — **checked after resolving the hostname**, so a name that points inward is caught as surely as a literal address (`ErrPrivateRedirect`). This is §5.11's webhook-sender posture applied to the one other place the plane dials out.
- **Bounded in time and size.** A 10 s timeout per request; the body is read through a 256 KiB limit and a larger one is an error (`ErrBodyTooLarge`), never a partial parse.
- **The failure mode is silence.** A feed that errors leaves the last good answer in place and logs at warn level with the feed *host* only — never the body, which is attacker-controlled text.
- **Nothing is executed and nothing is fetched twice.** The panel never updates itself (ADR-010); it renders a version, a class (patch/minor/major) and a notes URL. Drafts and pre-releases are ignored. The inbox item is written once per version (dedupe key `panel.update_available:<version>`), so a restart or a hostile feed flapping cannot flood owners.
- **Only owners hear it**, and only those who have not muted the kind.

**Residual risk.** The feed learns the panel's IP and its version (the
`User-Agent`), which is the minimum a version check can cost; an operator who
will not pay it sets `CYPHERD_UPDATE_CHECK=off`. The feed's *content* is
displayed to owners as text — a hijacked feed can therefore advertise a
plausible-looking version and a notes URL that is not ours. It cannot make the
panel fetch, install or run anything, which is the property that matters.

## 6. Cross-cutting controls (apply everywhere)

- **Secrets never in logs, errors, or API responses** — mask by default (ENGINEERING rule 20). Every log line carries resource IDs, never secret values (rule 4).
- **Constant-time comparison** for every token/secret check (rule 21).
- **mTLS for all internal traffic; no plaintext** (rule 23).
- **Idempotency everywhere on the bus** (rules 12–13) — makes replay a non-event and crash-recovery safe.
- **Parameterized SQL only** (sqlc) — injection closed structurally.
- **Least privilege** — every component gets the narrowest grant its job needs; the Docker socket on the agent is the one large, documented, understood grant.
- **Reproducible, scanned supply chain** — pinned dependencies (tech-stack), `govulncheck`/CodeQL/gitleaks/Trivy in `security.yml` (Phase 2), dependency justification per PR.

## 7. Assumptions and accepted risks

- We assume the **control-plane host is reasonably secured by the operator** (patched OS, restricted SSH *to the panel host itself*, disk encryption available). We harden what we own; we don't own the operator's host baseline.
- We accept that a **compromised control plane can deploy malicious images and read stored secrets** (§5.1). The architecture bounds this to "no fleet-wide root shell," which is the specific, measurable improvement over Coolify — not a claim of zero impact.
- We accept that a **compromised host is forfeit to its own agent's privileges** (§5.2); we contain blast radius, we don't reclaim an owned box from inside it.
- We accept **traffic analysis** on TB4 (§5.4) as unaddressed.

## 8. Phase 1 security requirements (acceptance criteria for the code that follows)

These are the concrete, checkable requirements the Phase 1 handshake code must satisfy. They are the security half of Phase 1's [acceptance gate](../roadmap.md).

1. **Enrollment** issues **single-use, short-TTL join tokens**; token comparison is constant-time; a used or expired token is rejected; a new server enrollment is an audited, surfaced event. (§5.3)
2. **The agent receives an mTLS client certificate** at enrollment and uses it for all subsequent traffic; there is **no other credential** and **no stored server credential on the plane**. (§5.1, §5.2)
3. **All agent↔plane traffic is mTLS, TLS 1.3, mutually verified against a pinned CA**; no plaintext path exists, not even in dev. (§5.4, rule 23)
4. **The wire protocol contains no arbitrary-command *verb*.** The boundary (refined by [ADR-011](../adrs/ADR-011-in-container-scheduled-tasks.md)) is *no verb that can execute outside a workload's own sandbox* — not "no command string ever." The Phase 1 surface is enrollment + heartbeat/state only, and the proto review checks that `work.*`/agent messages describe state, never an imperative "run this now" exec verb. Declarative workload config that includes a command — a container `HEALTHCHECK`, or a scheduled-task command the agent runs *inside the app's own unprivileged container* (ADR-011) — is desired state, not a verb: it grants no privilege beyond what deploying an image already does (§5.1), so it stays within the bound. A general host/privileged/cross-container exec verb remains forbidden. (§5.1)
5. **NATS subjects are authorized per-agent-identity**: an agent can publish only its own `state.*` and consume only its own `work.*`. (§5.2)
6. **Agent certificates are short-lived, self-renewing and revocable**; a revoked identity is refused a connection *and* a renewal, so it expires rather than persisting. (§5.2)
7. **The admin login path has rate limiting and secure session handling** from the first commit that exposes it; the admin account model supports TOTP. (§5.8)
8. **Secrets (including the CA key and the DB encryption key) are encrypted at rest and never logged**; the CA/encryption keys live outside any future auto-updater's blast radius. (§5.1, rule 20)
9. **JetStream streams are created with explicit retention/size limits** so log/metric/heartbeat churn is bounded from day one. (§5.9)
10. **The control plane guards its own disk/DB headroom** at boot and refuses to starve its own Postgres. (§5.9)

## 9. Threat → control → reference map

| Scenario | Primary control | Anchored in |
|---|---|---|
| §5.1 Plane compromise → fleet | No stored server creds; no exec verb; secrets encrypted outside updater | ADR-002, ADR-005, rule 20 |
| §5.2 Agent compromise → lateral | Per-identity subject authz **including reply inboxes** (`_INBOX_<cn>.>`) **and reply-subject validation on the plane's answer**; state-as-report; cert revocation; **90-day certs renewed at 2/3 lifetime over the authenticated channel, fresh key each time, revocation denies renewal** | ADR-002, ADR-003, rule 12, control-plane-hardening.md §1, agent-identity-and-tls.md §3 |
| §5.3 Join-token leak | Single-use, short-TTL, observable, constant-time check | ADR-002, rules 21–22 |
| §5.4 Channel MITM/replay | mTLS 1.3 pinned CA; outbound-only; idempotency | ADR-002, ADR-003, rule 23 |
| §5.5 Malicious template | Declarative data; CI gate; consent for privileged constructs; generated secrets | project-structure, dev/ci, ADR-007 (pending) |
| §5.6 Fork-PR secret exfil | Env-scoped preview secrets; untrusted-source policy; TTL | glossary (previews), Phase 3 spec |
| §5.7 Build/exec | Builds off the plane; resource caps; terminal audited & authorized | vision NN-5, ADR-001, ADR-002 |
| §5.8 Web/API | No SSR framework; auth+object-authz default; two-dimension rate limit with trusted-proxy client addressing; unforgeable trace ids; expired-session purge; owner+session-only log tail; **session-only deploy approval, rejection, break glass and deploy-protection policy writes**; masking | ADR-001, rules 20–21, control-plane-hardening.md §§2, 4, 5, 7, deploy-protection.md §5 |
| §5.9 Disk exhaustion/self-DoS | Desired-state GC; self-headroom guard; bounded retention; alerts | ADR-003, ADR-005, matrix V1 |
| §5.10 Mailbox-as-identity | Two factors to move an address; old address always notified; single-use hashed token; sessionOnly + rate limited | panel-mail.md §4–5, rules 20–21 |
| §5.11 Outbound webhook egress | Metadata-only payload; HMAC over raw bytes; sealed secret; no redirects; project-scoped authz; bounded retries | outbound-webhooks.md §4, §6, rule 20 |
| §5.13 Update check (outbound HTTP) | Opt-out; no private-range redirects (post-resolution); bounded body and timeout; last-good-on-failure; once-per-version inbox item; never self-updates | control-plane-hardening.md §3, ADR-010 |
| §5.12 DNS control / ownership | Sealed token; only records we created; derived content; verification recomputed not stored; panel-admin gated; request host pinned and path segments validated | dns-automation.md §3.1, §4.1, §4.4, rule 20 |

---

*Revisit this document when: a new driver or provider adds a boundary; the interactive terminal is built (§5.7 becomes live); preview environments are built (§5.6 becomes live); the template catalog opens (§5.5 becomes live); or `SECURITY.md` and the disclosure process ship with the repo going public (ADR-009 timing).*
