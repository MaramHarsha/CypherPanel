# CypherPanel — Personas

> Priority-ordered. When two personas' needs conflict, the higher one wins. The anti-persona is a design tool: features serving only them get cut.

## P1 — Sam, solo developer / indie hacker

**Context:** ships side projects and one revenue-making product; one or two $5–10 VPSes; no ops background and no desire for one.
**Pains today:** Heroku/Vercel pricing cliffs; Kubernetes is a non-starter; Coolify ate half the RAM of the box it managed; dashboards that hide what actually happened.
**Must-haves:** repo → URL with TLS in minutes; the panel and two apps comfortably on one cheap VPS; logs that answer "why did it crash"; managed Postgres + Redis; cheap preview for trying changes.
**Success moment:** pushes to `main` from a phone, sees the deploy succeed, does nothing else.

## P2 — Alex, agency lead

**Context:** 5–30 client applications across a handful of servers; two or three developers; clients occasionally get read access.
**Pains today:** juggling client isolation across tools; backup anxiety ("is the client's DB actually backed up? tested?"); handing a departing employee's SSH keys is a fire drill.
**Must-haves:** teams with roles per client project; scheduled, verifiable backups to S3 with tested restore; notification routing per project (client's Slack, agency's Discord); one-click templates for the WordPress/n8n/Plausible tier of requests; no shared SSH credentials to rotate — offboarding = deleting a user.
**Success moment:** onboards a new client project — server join, app deploy, backup schedule, client access — in under an hour.

## P3 — Priya, startup platform engineer

**Context:** 10–50 developers; left Vercel/Heroku over cost or data control; owns "the deploy experience" internally.
**Pains today:** PaaS bills scaling with headcount; needing previews, audit trails, and API automation without adopting Kubernetes yet; tools whose UI can do things their API can't.
**Must-haves:** preview environments per PR, automatically destroyed; complete REST API + tokens with scoped abilities; audit log of who deployed what; CI-triggered deploys; SSO (acceptable post-v1); the confidence that nothing routes through a vendor.
**Success moment:** deletes the "how to deploy" wiki page because the answer became "open a PR."

## P4 — Hendrik, self-hoster / homelab

**Context:** runs open-source apps (media, notes, automation) on a home server or mini-PC; values sovereignty; tolerance for tinkering but not for babysitting.
**Pains today:** hand-maintained compose files; certificate renewal surprises; portainer-style tools that manage containers but not *applications*.
**Must-haves:** big template catalog (the Coolify 361 is the bar); wildcard domains for `*.home.example.com`; low idle footprint (the box also does other things); everything works behind NAT.
**Success moment:** installs a template, and TLS, subdomain, and persistent volumes were simply handled.

## Anti-persona — enterprise Kubernetes platform team

Multi-region HA control planes, compliance regimes (SOC2 tooling, policy engines), fleet GitOps, service mesh. They already have Kubernetes and a team to run it. Features that serve only this profile are out of scope by [vision.md](../vision.md) — a `k8s` driver targeting *existing* clusters is the one sanctioned bridge, and it is post-v1.
