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

### 5.2 Single-agent compromise → lateral movement

**Attack.** An attacker gets RCE on one server (a vulnerable deployed app escapes its container, or the host was already dirty). The agent there holds the Docker socket (TB5, root-equivalent) and a valid mTLS identity (A-tier trust on TB4).

**Property that must hold.** A compromised agent can harm *its own* server (it already could — it holds that host's Docker socket) but must have **minimal leverage over other servers or the control plane.**

**Controls.**
- **Agent identity is scoped to its own server.** The plane authorizes `state.*`/`work.*` per-agent; one agent's certificate does not let it subscribe to another server's work or publish another server's state. NATS subject authorization is per-identity. `[Phase 1: bus authz]`
- **The plane trusts observed state as a *report*, not a command.** A lying agent can misreport *its own* status (causing the scheduler to redeploy, at worst) but cannot mutate desired state or another server's reconciliation. Desired state is written only by the Core API behind user auth (ADR-005). `[Phase 1]`
- **Certificate revocation** — a burned agent's cert can be revoked at the plane, cutting it off; enrollment of its replacement is a fresh join token. `[Phase 1: mint short-lived certs; revocation list design]`
- **Short-lived agent certificates with rotation** (renew over the authenticated channel) bound the window a stolen cert is useful. `[Phase 1: cert TTL decision]`
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

**Residual risk.** A determined authenticated attacker can still generate load; rate limiting and per-team quotas (post-v1) bound it. The self-inflicted case — the common one — is designed out.

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
6. **Agent certificates are short-lived and revocable**; a revoked identity is refused. (§5.2)
7. **The admin login path has rate limiting and secure session handling** from the first commit that exposes it; the admin account model supports TOTP. (§5.8)
8. **Secrets (including the CA key and the DB encryption key) are encrypted at rest and never logged**; the CA/encryption keys live outside any future auto-updater's blast radius. (§5.1, rule 20)
9. **JetStream streams are created with explicit retention/size limits** so log/metric/heartbeat churn is bounded from day one. (§5.9)
10. **The control plane guards its own disk/DB headroom** at boot and refuses to starve its own Postgres. (§5.9)

## 9. Threat → control → reference map

| Scenario | Primary control | Anchored in |
|---|---|---|
| §5.1 Plane compromise → fleet | No stored server creds; no exec verb; secrets encrypted outside updater | ADR-002, ADR-005, rule 20 |
| §5.2 Agent compromise → lateral | Per-identity subject authz; state-as-report; cert revocation | ADR-002, ADR-003, rule 12 |
| §5.3 Join-token leak | Single-use, short-TTL, observable, constant-time check | ADR-002, rules 21–22 |
| §5.4 Channel MITM/replay | mTLS 1.3 pinned CA; outbound-only; idempotency | ADR-002, ADR-003, rule 23 |
| §5.5 Malicious template | Declarative data; CI gate; consent for privileged constructs; generated secrets | project-structure, dev/ci, ADR-007 (pending) |
| §5.6 Fork-PR secret exfil | Env-scoped preview secrets; untrusted-source policy; TTL | glossary (previews), Phase 3 spec |
| §5.7 Build/exec | Builds off the plane; resource caps; terminal audited & authorized | vision NN-5, ADR-001, ADR-002 |
| §5.8 Web/API | No SSR framework; auth+object-authz default; rate limit; masking | ADR-001, rules 20–21 |
| §5.9 Disk exhaustion/self-DoS | Desired-state GC; self-headroom guard; bounded retention; alerts | ADR-003, ADR-005, matrix V1 |

---

*Revisit this document when: a new driver or provider adds a boundary; the interactive terminal is built (§5.7 becomes live); preview environments are built (§5.6 becomes live); the template catalog opens (§5.5 becomes live); or `SECURITY.md` and the disclosure process ship with the repo going public (ADR-009 timing).*
