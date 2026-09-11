# CypherPanel — Vision

## Why CypherPanel exists

Self-hosting deployments today forces a bad choice. Coolify gives you breadth — hundreds of templates, every database, every notification channel — carried by a heavyweight Laravel stack that orchestrates fleets over SSH and eats a small VPS alive. Dokploy gives you a polished, modern experience — but welds itself to Docker Swarm and runs builds on the same node that runs the panel. Both are excellent products with the same underlying flaw: the control plane and the workload plane are tangled together.

CypherPanel exists to be the tool both of them are trying to be: **the deployment platform you stop thinking about.** Push code, get a URL, sleep well.

## Who it is for

In priority order (full detail in [product/personas.md](product/personas.md)):

1. **Solo developers and indie hackers** deploying side projects and small products on cheap VPSes.
2. **Small teams and agencies** running many client apps across a handful of servers.
3. **Startup platform engineers** who left Heroku/Vercel for cost or control and need previews, API access, and audit trails.
4. **Self-hosters** running open-source apps from one-click templates.

Explicit anti-persona: enterprise platform teams needing multi-region HA control planes, compliance regimes, or Kubernetes fleet management. They have Kubernetes; we are not building it again.

## What using it should feel like

- **Fast to first success.** Fresh VPS to first deployed app with a URL and TLS in under 10 minutes, most of which is DNS propagation.
- **Calm.** The panel tells you what state things are in and what it's doing about it. No mystery spinners, no "check the server logs to find out what really happened."
- **Honest.** Every action visible in the UI is a documented API call. Every automatic behavior is inspectable. Nothing magical, nothing hidden.
- **Light.** Installing CypherPanel on a server should feel like adding a tool, not adopting a platform.

## Non-negotiables

These are constraints, not aspirations. A feature that violates one is wrong even if it works:

1. **Lightweight, with numbers.** Agent idle footprint < 50 MB RSS. Control plane (excluding Postgres) < 300 MB RSS idle. One binary + one database to install.
2. **No stored SSH credentials, ever.** Agents dial home over mTLS ([ADR-002](adrs/ADR-002-agent-dial-home-no-ssh.md)). A compromised control plane must not yield shell access to every connected server.
3. **API-first.** No feature ships UI-only. If the API can't do it, it doesn't exist.
4. **Zero-downtime deploys by default.** Opting *into* downtime is allowed; getting it by surprise is not.
5. **The control plane never runs user workloads or builds.** Not even "just this once."

## Explicitly out of scope

- A Kubernetes distribution or general cluster manager (a `k8s` driver may target *existing* clusters later).
- Serverless / function runtimes (v1).
- A CI system — we integrate with CI via webhooks and API, we don't replace it.
- Multi-region / HA control plane (v1). One control plane node, many workers.
- SaaS billing and metering (the open-source product comes first; cloud concerns must never leak into core, the way Stripe code is threaded through Coolify).

## What success looks like

- A $5 VPS runs the control plane *and* two deployed apps comfortably.
- A team migrates off Coolify or Dokploy and loses zero capabilities they used.
- The API is good enough that someone builds a competing UI on top of it — and that's fine.
