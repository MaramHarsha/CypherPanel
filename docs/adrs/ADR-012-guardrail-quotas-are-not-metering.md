# ADR-012: Guardrail quotas are not metering

- **Status:** Accepted
- **Date:** 2026-09-07

## Context

[vision.md](../vision.md) lists **"SaaS billing and metering"** under
*Explicitly out of scope*, and gives its reason in the parenthesis: *"the
open-source product comes first; cloud concerns must never leak into core, the
way Stripe code is threaded through Coolify."*

[metrics-and-usage.md](../features/metrics-and-usage.md) §8 read that line
carefully and stayed on the safe side of it, by writing down three rules its own
design satisfies — no monetary concept anywhere, nothing that ever refuses an
action, and a CSV that ends where a spreadsheet begins. It then named the thing
those rules deliberately excluded: *"Metering with teeth is what 'metering'
means in that vision line, and this has none."*

[resource-quotas.md](../features/resource-quotas.md) is a feature with teeth. It
caps the memory, disk and live-preview count one Project or one Team may
consume, and it **refuses new work at the cap**. That is, by metrics-and-usage's
own definition, on the other side of the line — so it cannot be built on a
feature spec's authority. CLAUDE.md rule 2 says architectural decisions are not
re-litigated in feature specs, and ENGINEERING rule 31 says where they live.
This is that decision.

The failure the quota exists to stop is real and is not hypothetical. The panel
can already cap **one container** — `memory_limit_mb` on an application or a
database has existed since `0012_app_resource_limits.sql`. What no number in the
codebase can express is the **aggregate**: an agency running eleven clients on
four boxes can cap every container individually and still watch one project open
forty preview environments, keep six revisions of images per application, and
fill the disk a paying client's database writes to. Every individual limit was
respected, and the outage happened anyway.

## Decision

**A quota is a guardrail, not a meter, and the distinction is enforceable rather
than rhetorical.** Resource quotas ship, bounded by three rules that make them
structurally incapable of becoming billing:

1. **No monetary concept exists anywhere in the feature.** No price, no rate, no
   currency, no plan, no invoice, no payment integration — not in the schema,
   not in the API, not in the UI. A quota is denominated in bytes and in counts.
   An operator who wants a bill multiplies our numbers by their own, in their
   own spreadsheet, off the panel.

2. **A quota is set by an operator on infrastructure they own.** There is no
   grant, no entitlement, no external system of record, no upgrade flow and no
   tier. The panel never learns what anything costs, and nothing in the feature
   is reachable by a caller who is not already an administrator of the thing
   being capped.

3. **It cannot become billing by increments.** Adding a price column to a quota
   row, an entitlement source outside the panel, or a tier concept is a change
   to the vision's out-of-scope list in its own right, and needs its own
   recorded decision. This buys enforcement, not a foundation for commerce.

**The panel already does exactly this kind of thing, and nobody calls it
billing.** A Freeze Window refuses a deploy. An approval requirement parks one.
`ON DELETE RESTRICT` on a registry credential refuses a deletion and names what
is using it. Deploy protection is a guardrail with teeth, set by the operator,
enforced by the plane, and overridable under Break Glass. A quota is the same
governance mechanism with a resource dimension where protection has a clock.

## Consequences

**What this permits.** `resource_quotas` and `quota_states`, an admission check
at the four call sites where consumption is committed, warn-at-90 /
refuse-at-100 with the transition announced once rather than every deploy, and a
bounded, audited owner override for the night the fix has to ship anyway.

**What it still forbids.** Everything in rule 1, and the whole shape of rule 2.
A reviewer reading a future PR can check it against those two lists without
re-deriving this argument.

**metrics-and-usage §8 stays true.** That feature still never refuses anything;
its sentence about quota enforcement being banned gains a forward reference to
here rather than being deleted, because the sentence was correct when it was
written and the thing that changed is this decision, not that design.

**The alternative rejected:** defer quotas indefinitely and leave the aggregate
gap open. That was the position until now, and it is defensible only while the
panel has no way to measure aggregate consumption. metrics-and-usage shipped
that measurement, which is what turns "we cannot bound a tenant" from a
limitation into a choice — and leaving the choice unmade means the agency
persona's outage keeps happening with every individual limit respected.
