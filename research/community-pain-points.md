# Research: Community Pain Points — Coolify & Dokploy

> What users actually vote for and complain about. Gathered 2026-07-17 from both projects' issue trackers **sorted by 👍 reactions** — effectively community ballots — plus public discussions. (Reddit blocks automated crawling; GitHub reactions are the higher-signal source regardless.) Every new gap found here has a corresponding row in [feature-matrix.md](../docs/product/feature-matrix.md).

## Validated bets — the community wants what we already planned

| Community signal | Source | Our coverage |
|---|---|---|
| Kubernetes integration | coolify#45 — open for years, still voted | `k8s` driver, post-v1 (driver seam in project-structure.md) |
| Caddy support | Dokploy's top-voted request | ADR-004 proxy driver interface |
| Autoscaling (HPA) + compose replicas | Dokploy high-voted | roadmap post-v1 scale-out ladder |
| Dedicated build server | Dokploy (shipped it; was top-voted) | builder-role agents, V1 — ours is the default, not an add-on |
| Rollbacks | Dokploy (shipped; top-voted) | V1 |
| Backup manager UI | coolify#2389 | Backups view (Phase 3–4) |
| Migration from Coolify | dokploy#3098 | importer row, V1.x |
| OIDC/SSO, pre/post-deploy hooks, cron, multiple admins | various top-voted | matrix rows (V1/V1.x/Later) |

## New gaps discovered (now recorded as matrix rows)

1. **Safe self-update — the #1 trust wound.** Evidence: [coolify discussion #3687](https://github.com/coollabsio/coolify/discussions/3687) — the web-UI update button ran the updater twice and destroyed `.env` including encryption keys; coolify#7193 — Traefik proxy broken after update (top-voted bug); coolify#7599 — version-to-version update failure; a public review titled "Coolify: Great Until Something Breaks". **Consequence for ADR-010:** update = pre-update snapshot → atomic apply → verified health → else rollback; update lock against double-trigger; secrets stored outside the updater's blast radius.
2. **Escape hatches.** coolify#2549 (container labels not applying) and coolify#1092 (host network mode) are top-voted: power users need raw Docker passthrough (labels, networks, extra options) without the panel fighting or forgetting them.
3. **Move a resource between servers.** Top Dokploy request; nearly free under desired state (reassign → reconcile → migrate volumes).
4. **IPv6 first-class.** coolify#2484 (can't add server via IPv6) is a top-voted bug; IPv6-only VPSes are the cheapest machines — P1 territory.
5. **External secret managers** (Vault, Infisical, Doppler) — rising Dokploy request; same customer-brings-token integration pattern as cloud providers.
6. **Importer nuance:** dokploy#3098 asks for **in-place adoption of running containers without downtime** — install alongside, detect, take over management. Harder and far more valuable than a config-only importer.

## Reddit findings (user-conducted research, 2026-07-17)

Threads across r/coolify, r/selfhosted, r/nextjs, r/AgenticSaasHQ — gathered manually (Reddit blocks crawlers). Reddit surfaces a different signal than GitHub reactions: **production outage reports and adoption blockers rather than feature votes** — and its #1 complaint never appears in GitHub's top-voted list. Both layers are needed.

### New gaps

1. **Silent disk exhaustion is the #1 production killer for both tools.** Build cache + orphaned images fill the disk with no warning until the panel itself crashes (reported: `coolify` + `coolify-db` containers in a crash loop because Postgres couldn't write its lock file). Users discover the problem *after* the outage. Consequences (matrix row upgraded to **V1**): disk threshold alerts before failure; automatic pruning policies; **desired-state GC** — the agent knows exactly which images/containers/networks desired state references, so pruning is principled rather than heuristic; the control plane self-protects (reserved disk headroom for its own database).
2. **Silent build failures.** Coolify hides useful build output unless `BUILDKIT_PROGRESS=plain` is manually enabled. Consequence: verbose, streamed build logs are the **default** (new matrix row) — no hidden verbosity toggles.
3. **Stale-container deploys — Dokploy's worst bug.** Deploy reports success but the old container keeps serving; users describe it as unresolved for months with "stop and rebuild manually" as the only workaround. This is imperative deployment's signature failure and the strongest field validation of ADR-005: for us, "deploy succeeded" is only assertable when *observed* state confirms the new revision serving and the old drained — the failure mode is definitionally impossible, and rollout reconcilers carry idempotency/convergence tests (ENGINEERING rule 13).
4. **Proxy/network naming bug (Coolify).** The proxy loses track of which Docker network to use under load → CORS errors recurring every few hours; the fix is explicitly naming the network. Lesson recorded in [coolify.md](coolify.md): all agent-created networks get deterministic explicit names, referenced by name everywhere, never by Docker's auto-naming.
5. **Heavy framework builds crash modest hosts.** Next.js/Nuxt builds spike CPU/RAM enough to take down single-server VPSes; the community's own workarounds (swapfile, separate build server) point at our architecture. Builder-role agents are the structural fix; on single-server setups build containers get resource caps; framework presets (`.dockerignore`, Next.js `output: "standalone"`, memory guidance) become a matrix row.
6. **Panel-framework CVEs are a trust event.** A critical Next.js/RSC vulnerability made Dokploy's own dashboard an attack surface, compounded by unclear communication about whether a patched release had shipped. Two consequences: **(a) architectural** — our panel has no server-side web framework; the UI is static assets served from a Go binary, so this CVE class largely doesn't apply (an unplanned security validation of ADR-001); **(b) process** — ship `SECURITY.md` with a disclosure policy and a committed advisory/patch communication path when the repo goes public (same timing as ADR-009).
7. **Control-plane disaster recovery needs a runbook, not just a feature.** Professional users roll their own panel-backup-to-S3 strategies because neither tool documents DR. Our panel-backup row covers the mechanism; a user-facing DR runbook joins the Phase 3–4 docs.

### Validations from Reddit

Missing team/RBAC separation hurts Dokploy adoption (our V1 rows) · licensing paywall rumors erode Dokploy trust (ADR-009's "cleaner than Dokploy" requirement) · manual unmonitored OS updates flagged (our Later row) · "get a separate build server" as the community's own advice (our default architecture) · multi-server/HA maturity doubts on Coolify (our honest v1 scope: one control plane, many workers).

## Competitive intel

- **Coolify v5** (coolify#5685, 176 reactions) is the single most-anticipated item in their tracker — a major rewrite is coming. The architectural window is real but not indefinite.
- **Dokploy ships its top-voted requests fast** (several already marked Completed). Their strength is feature velocity; ours must be the architectural wedge they can't follow without a rewrite.

## Sources

- https://github.com/coollabsio/coolify/issues?q=is%3Aissue+sort%3Areactions-%2B1-desc
- https://github.com/Dokploy/dokploy/issues?q=is%3Aissue+sort%3Areactions-%2B1-desc
- https://github.com/Dokploy/dokploy/issues/3098
- https://github.com/coollabsio/coolify/discussions/3687
- https://www.youtube.com/watch?v=yt8xd0I-FVA
