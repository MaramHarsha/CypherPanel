# ADR-009: Apache-2.0 for the whole repository

- **Status:** Accepted
- **Date:** 2026-07-20

## Context

The license shapes community and monetization from day one ([roadmap](../roadmap.md)),
and it must be decided before the repo goes public. What weighs on it:

- **Trust is the product.** CypherPanel competes on being the self-hosted
  panel people can rely on. Coolify's plain Apache-2.0 is the trust benchmark
  in this space; Dokploy's mixed model (open code beside proprietary
  directories) is a documented community irritant and a standing footgun for
  contributors — the roadmap explicitly demands "cleaner than Dokploy's
  mixed model". Our own hard rule 1 exists because of that mixing.
- **The users we court are self-hosters and small teams** moving off
  Heroku/Vercel bills. Copyleft friction (legal review, linking anxiety) hits
  exactly them; a cloud hyperscaler deciding to resell a panel is not a
  realistic v1 threat worth taxing every legitimate adopter for.
- **Patent grant matters.** Apache-2.0's explicit patent license is the
  difference for companies self-hosting this next to production workloads;
  MIT is silent there.
- **Monetization** (roadmap Later: a hosted Cloud offering, support) does not
  depend on copyleft. It depends on running the best hosted version and on
  the **trademark**: anyone may fork the code, nobody may call their fork
  CypherPanel.

## Decision

**The entire repository is licensed Apache-2.0. No open-core split, no
dual-licensed directories, no CLA at v1** — contributions are accepted under
Apache-2.0 (DCO sign-off when the repo goes public). The `LICENSE` file at
the repo root is the single license of record. The brand is protected by
trademark, not by the code license.

## Alternatives considered

- **MIT.** Equally adoption-friendly, but no patent grant and no
  contribution-license clarity (Apache-2.0 §5); nothing gained over Apache.
- **AGPL-3.0.** Deters hosted-competitor forks in theory, but its real-world
  effect is deterring the enterprises and cautious self-hosters we want,
  while a determined competitor works around it. The Coolify comparison
  would read "they're Apache, CypherPanel is AGPL" — a loss on the axis we
  compete on.
- **Open-core / BSL / FSL.** Recreates exactly the Dokploy trust problem
  this project cites as a pain point. If a future Cloud tier ever needs
  proprietary components, they live in a separate private repo — this repo
  stays whole — and that move would still need a superseding ADR.

## Consequences

- `LICENSE` (Apache-2.0) at the repo root; per-file headers are not required
  (the LICENSE file governs), keeping source noise-free.
- All current code — written before this ADR — is licensed as of this commit;
  the git history has a single author, so no relicensing consent is needed.
- A trademark policy (name + logo usage) is the anti-freeride lever and gets
  written before any public release announcement.
- Third-party dependencies are all Apache-2.0/MIT/BSD-compatible (Go
  ecosystem norm); a dependency audit gate joins the release checklist.
