// Settings · Audit log (canvas 3g).
//
// SOURCING NOTE, because it matters for a later diff: 3g lives in turn 3 of the
// design canvas, and its dark twin in turn 5. Neither survives the 256 KiB read
// cap on `CypherPanel Redesign.dc.html`, so this screen is not transcribed from
// its card the way registries (13d) and protection (13k) were. It is built from
// the handoff's canonical table spec instead — "header row in mono 10px .1em
// uppercase faint; rows separated by 1px #e4dfd5; first row often 1.5px ink top
// rule; error rows get faint red gradient wash" — plus the status-chip and
// filter idioms this codebase already carries. When turn 3 is readable, the
// diff against it should be a token check, not a rewrite.
//
// What the log is (audit-log.md): one immutable row per sensitive action — who
// did what to which resource, from where, and whether it worked. Reading needs
// no role gate because scope IS the authorization: your teams' rows, your own
// actions wherever they landed, plus panel-level rows for a panel admin. So
// there is no "you may not read this" state to design; a team you do not belong
// to simply answers an empty page.
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { useListAuditEvents } from "@/api/gen/audit/audit";
import type { AuditEvent } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/audit")({ component: AuditTab });

// The families the API documents, offered as a filter because "everything that
// happened" is rarely the question — "what happened to deploys" is.
const FAMILIES = [
  "auth",
  "token",
  "user",
  "team",
  "server",
  "deploy_key",
  "project",
  "environment",
  "application",
  "database",
  "deploy",
  "protection",
  "registry",
  "compose_stack",
  "backup",
  "shared_variable",
  "notifier",
  "webhook_endpoint",
  "panel",
] as const;

function AuditTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "audit" }]);
  const [family, setFamily] = useState("");
  const [actor, setActor] = useState("");
  // Failure is an outcome rather than a separate verb, so "everything that was
  // refused" is one predicate over the whole vocabulary — which is exactly the
  // filter worth having on screen.
  const [failuresOnly, setFailuresOnly] = useState(false);

  const events = useListAuditEvents({
    ...(family ? { action: family } : {}),
    ...(actor.trim() ? { actor: actor.trim() } : {}),
    ...(failuresOnly ? { outcome: "failure" as const } : {}),
    limit: 100,
  });
  const filtered = Boolean(family || actor.trim() || failuresOnly);
  const clearFilters = () => {
    setFamily("");
    setActor("");
    setFailuresOnly(false);
  };

  return (
    <div className="space-y-3">
      <Eyebrow>Audit log</Eyebrow>
      <p className="max-w-xl text-[12.5px] leading-[1.5] text-text-mid">
        One row per sensitive action — who did what to which resource, from where, and whether it worked. Details carry
        key names and reasons, never a secret value. You see your teams’ rows and your own actions wherever they landed.
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <select
          value={family}
          onChange={(e) => setFamily(e.currentTarget.value)}
          className="mono rounded-md border border-border-input bg-surface px-2.5 py-2 text-[12px]"
          aria-label="Filter by action family"
        >
          <option value="">all actions</option>
          {FAMILIES.map((f) => (
            <option key={f} value={f}>
              {f}
            </option>
          ))}
        </select>
        <Input
          value={actor}
          onChange={(e) => setActor(e.currentTarget.value)}
          placeholder="actor — an email"
          className="mono max-w-[220px]"
          aria-label="Filter by actor"
        />
        <Button
          type="button"
          variant={failuresOnly ? "primary" : "secondary"}
          size="sm"
          aria-pressed={failuresOnly}
          onClick={() => setFailuresOnly((v) => !v)}
        >
          Refused only
        </Button>
      </div>

      <PageState
        query={events}
        // A page is an object rather than an array, so emptiness has to be
        // spelled out — the default check would never fire.
        isEmpty={(page) => page.events.length === 0}
        empty={
          filtered ? (
            <EmptyState
              glyph="≡"
              title={missTitle(family, actor.trim(), failuresOnly)}
              hint="The log itself is never empty — first-run setup is entry #1."
              action={
                <Button type="button" size="sm" variant="secondary" onClick={clearFilters}>
                  Clear filters
                </Button>
              }
            />
          ) : (
            <EmptyState
              title="Nothing recorded yet"
              hint="Sensitive actions land here as they happen — sign-ins, deploys, member changes, credential edits."
            />
          )
        }
      >
        {(page) => <AuditTable events={page.events} />}
      </PageState>
    </div>
  );
}

/**
 * The miss states its own filter (15a: "Nothing from priya@ in the last 24
 * hours") — a generic "no matching entries" makes the reader work out which of
 * the three controls above them produced the nothing. The canvas's second verb,
 * "Widen to 7 days", has nothing to act on here: this list is unbounded in
 * time, so there is no window to widen — only filters to clear.
 */
function missTitle(family: string, actor: string, failuresOnly: boolean): string {
  const scope = [failuresOnly ? "refused" : null, actor ? `from ${actor}` : null, family ? `in ${family}` : null]
    .filter(Boolean)
    .join(" ");
  return `Nothing ${scope}`;
}

// Below `sm` the five broadsheet columns become a stacked card like every other
// list in the panel (ui-principles §9): at 360px the table put Resource, Detail
// and the trace id behind a sideways drag. From `sm` the 720px minimum stands,
// so the columns keep the width they need.
const GRID = "sm:grid sm:grid-cols-[6rem_1.1fr_9rem_1.25fr_1.6fr] sm:items-start sm:gap-4";

function AuditTable({ events }: { events: AuditEvent[] }) {
  return (
    <div className="overflow-x-auto">
      <div className="sm:min-w-[720px]">
        {/* The canvas's table header: mono 10px, .1em tracking, uppercase,
            faint — and gone with the grid, since headers over a stack of cards
            are noise. */}
        <div className={cn(GRID, "eyebrow hidden pb-2")}>
          <span>When</span>
          <span>Actor</span>
          <span>Action</span>
          <span>Resource</span>
          <span>Detail</span>
        </div>
        <ul className="max-sm:space-y-2.5">
          {events.map((e, i) => (
            <AuditRow key={e.id} e={e} first={i === 0} />
          ))}
        </ul>
      </div>
    </div>
  );
}

function AuditRow({ e, first }: { e: AuditEvent; first: boolean }) {
  const refused = e.outcome === "failure";
  const reason = typeof e.detail?.reason === "string" ? e.detail.reason : "";
  // Everything except the reason, so the row shows what changed without
  // repeating the refusal sentence beside it.
  const extras = Object.entries(e.detail ?? {})
    .filter(([k]) => k !== "reason")
    .map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : String(v)}`)
    .join(" · ");

  return (
    <li
      className={cn(
        // A phone card (14b): the five fields stack, and the rules that
        // separate broadsheet rows become the card's own edge.
        "max-sm:space-y-1.5 max-sm:rounded-[10px] max-sm:bg-surface max-sm:p-3.5",
        GRID,
        // The canvas's rules: 1px between rows, a 1.5px ink top rule on the
        // first, and a faint red gradient wash on an error row.
        "sm:py-2.5",
        first ? "sm:border-t-[1.5px] sm:border-t-border-strong" : "sm:border-t sm:border-t-border",
        refused ? "max-sm:border-[1.5px] max-sm:border-danger/40" : "max-sm:border max-sm:border-border",
        refused && "bg-gradient-to-r from-danger/[.06] to-transparent",
      )}
    >
      <span className="mono block text-[11.5px] text-text-faint sm:whitespace-nowrap" title={e.at}>
        {relativeTime(e.at)}
      </span>
      <span className="block min-w-0 text-[12.5px] text-text">
        <span className="block truncate">{e.actor.label || e.actor.kind}</span>
        {/* A token is a way to act, not an identity — so when one was used it is
            named beside its owner, because it is the credential to revoke. */}
        {e.actor.token_id ? <span className="mono block text-[10.5px] text-text-faint">token {e.actor.token_id}</span> : null}
      </span>
      <span className="block sm:whitespace-nowrap">
        <span
          className={cn(
            "mono inline-block rounded border px-2 py-[3px] text-[10.5px] uppercase tracking-[.02em]",
            refused
              ? "border-danger/40 bg-danger/[.08] text-danger"
              : "border-border bg-raised text-text-mid",
          )}
        >
          {e.action}
        </span>
      </span>
      <span className="block min-w-0 text-[12.5px] text-text-mid">
        <span className="mono text-[11px] text-text-faint">{e.resource.kind}</span>{" "}
        <span className="break-all">{e.resource.name || e.resource.id}</span>
      </span>
      <span className="block min-w-0 text-[12px] text-text-mid">
        {refused && reason ? <span className="block text-danger">{reason}</span> : null}
        {extras ? <span className="mono block break-all text-[11px] text-text-faint">{extras}</span> : null}
        {e.trace_id ? <span className="mono block text-[10.5px] text-text-faint">{e.trace_id}</span> : null}
      </span>
    </li>
  );
}
