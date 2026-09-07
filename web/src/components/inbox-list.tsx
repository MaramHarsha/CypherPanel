// The inbox feed — one list, two homes: the bell's panel (canvas 13u) and the
// /inbox page the phone's bottom bar leads to (canvas 14e). Both read the same
// generated hooks and render the same rows; what differs is how a row offers
// its action — a text link in the panel's meta line, pills under the title on
// the page, where a thumb needs something to land on.
//
// Three decisions the feed is built around (notification-inbox.md §7):
//
//   · reading is EXPLICIT. Opening the panel or the page marks nothing;
//     following an item's link marks that one and navigates. Auto-clearing on
//     open would destroy the only signal the bell carries, and would make
//     "Mark all read" meaningless;
//   · a digest is one row and one unread however many events it holds, and it
//     carries no action link — a rollup of three backups has no single thing
//     to open;
//   · the marker carries the word as well as the shape (14g: "every dot
//     carries the word"): square red says "error", round green says "ok".
//     It is not StatusDot, whose vocabulary is a resource's — a backup
//     notification is not "running".
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useCallback, useMemo, useState } from "react";
import {
  getGetInboxUnreadCountQueryKey,
  getListInboxQueryKey,
  useListInbox,
  useMarkAllInboxRead,
  useMarkInboxItemRead,
} from "@/api/gen/inbox/inbox";
import type { InboxItem } from "@/api/gen/model";
import { useListProjects } from "@/api/gen/projects/projects";
import { useApproveDeployment, useRejectDeployment } from "@/api/gen/protection/protection";
import { EmptyState } from "@/components/empty-state";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

/** §6 caps a page at 100 server-side; asking for more is asking for the cap. */
export const INBOX_MAX = 100;

export type KindFilter = "all" | "deploy" | "backup";
export type SeverityFilter = "all" | "error";

/**
 * `unreadOnly` is the API's own filter (?unread). Kind and severity have no
 * server parameter (notification-inbox.md lists "filters beyond ?unread" as
 * out of scope), so they narrow the rows already on screen — honest for a
 * feed that is never more than 100 rows deep.
 */
export interface InboxFilters {
  unreadOnly: boolean;
  kind: KindFilter;
  severity: SeverityFilter;
}

export const NO_FILTERS: InboxFilters = { unreadOnly: false, kind: "all", severity: "all" };

export function isFiltered(f: InboxFilters): boolean {
  return f.unreadOnly || f.kind !== "all" || f.severity !== "all";
}

function matches(it: InboxItem, f: InboxFilters): boolean {
  if (f.kind !== "all" && !it.kind.startsWith(`${f.kind}.`)) return false;
  if (f.severity !== "all" && it.severity !== f.severity) return false;
  return true;
}

/**
 * A mark changes both the feed and the bell, so both keys go. The list key is
 * taken without params on purpose: TanStack matches key prefixes, so this one
 * invalidation covers the filtered variant as well as the unfiltered one, and
 * the two can never disagree about the count.
 */
export function useInboxRefresh(): () => void {
  const qc = useQueryClient();
  return useCallback(() => {
    void qc.invalidateQueries({ queryKey: getGetInboxUnreadCountQueryKey() });
    void qc.invalidateQueries({ queryKey: getListInboxQueryKey() });
  }, [qc]);
}

/** The badge is exact to 99 and then says so rather than lying by rounding. */
export function badgeLabel(n: number): string {
  return n > 99 ? "99+" : String(n);
}

/** The accent count beside the word "Inbox" (13u/14e). Never a zero. */
export function CountPill({ count, className }: { count: number; className?: string }) {
  if (count <= 0) return null;
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full bg-accent px-[7px] py-px font-mono text-[10px] font-medium leading-[1.5] text-accent-fg",
        className,
      )}
      aria-label={`${count} unread`}
    >
      {badgeLabel(count)}
    </span>
  );
}

/**
 * "Mark all read" as the canvas draws it (13u/14e): a quiet text action, not
 * a pill — clearing a count is housekeeping, and a pill would outrank the
 * rows it clears. Still a 6-state button underneath (10b): the verb in
 * progress, a beat of "✓ All read", the retry on failure, and a reason when
 * there is nothing to mark.
 */
export function MarkAllRead({ unread, className }: { unread: number; className?: string }) {
  const refresh = useInboxRefresh();
  const markAll = useMarkAllInboxRead({ mutation: { onSuccess: refresh } });
  const state = useMutationActionState(markAll);
  return (
    <ActionButton
      size="sm"
      variant="ghost"
      state={state}
      busyLabel="Marking…"
      successLabel="All read"
      failedLabel="Retry"
      disabledReason={unread === 0 ? "Nothing is unread" : undefined}
      onClick={() => markAll.mutate()}
      className={cn(
        "h-auto rounded-none border-0 bg-transparent px-0 py-0.5 text-[11.5px] font-normal hover:bg-transparent hover:shadow-none",
        // Only the resting label is faint: success and failure keep the
        // green and red the vocabulary gives them.
        state === "idle" && "text-text-faint hover:text-text",
        className,
      )}
    >
      Mark all read
    </ActionButton>
  );
}

/**
 * A digest's time is the hour it was cut ("04:02 · digest", 13u/14e) — a
 * rollup happens at a known clock time and "6h ago" would hide it. Only today's,
 * though: yesterday's "04:02" would read as this morning's.
 */
function whenLabel(it: InboxItem): string {
  if (it.digest) {
    const d = new Date(it.created_at);
    const now = new Date();
    if (!Number.isNaN(d.getTime()) && d.toDateString() === now.toDateString()) {
      return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
    }
  }
  return relativeTime(it.created_at);
}

/**
 * The deployment a parked-approval row is about. An item carries a link and no
 * subject (notification-inbox.md: an item links, it never acts), but the plane
 * builds that link out of the deployment id and validates it at write time — so
 * the id is there, spelled as a path.
 */
function parkedDeploymentId(it: InboxItem): string | undefined {
  if (it.kind !== "deploy.awaiting_approval") return undefined;
  const query = it.link.slice(it.link.indexOf("?") + 1);
  const dep = new URLSearchParams(query).get("dep");
  return dep?.startsWith("dep_") ? dep : undefined;
}

/** id → name for the meta line ("atlas-crm · 2 min"); InboxItem carries only the id. */
function useProjectNames(): Map<string, string> {
  const projects = useListProjects().data;
  return useMemo(() => new Map((projects ?? []).map((p) => [p.id, p.name])), [projects]);
}

export function InboxList({
  filters,
  limit,
  layout,
  onClearFilters,
  onMore,
  onNavigate,
  rowsRef,
  footer,
}: {
  filters: InboxFilters;
  /** How many rows to ask for; the page grows it, the panel holds it at one page. */
  limit: number;
  /** `panel` puts the action in the meta line; `page` gives it pills (14e). */
  layout: "panel" | "page";
  onClearFilters: () => void;
  /** Offered when the server has older rows and the caller can take a bigger page. */
  onMore?: () => void;
  /** Called when a row's link is followed — the panel closes itself. */
  onNavigate?: () => void;
  /** The page's j/k row model (lib/keys.ts useRowNavigation) attaches here. */
  rowsRef?: (el: HTMLElement | null) => void;
  /** The line under the last row — "Showing the 20 most recent · Open inbox →". */
  footer?: (page: { shown: number; more: boolean }) => ReactNode;
}) {
  const page = useListInbox({ unread: filters.unreadOnly || undefined, limit });
  const refresh = useInboxRefresh();
  const names = useProjectNames();
  const wide = layout === "page";

  return (
    <PageState
      query={page}
      isEmpty={(d) => d.items.length === 0}
      skeletonRows={4}
      skeletonDot
      empty={
        // Filtered-to-zero is a DIFFERENT state from an empty inbox
        // (ui-principles §7): one is "nothing has happened yet", the other
        // is "you have read everything", and a single message for both
        // would be wrong half the time.
        filters.unreadOnly ? (
          <NothingUnread onClear={onClearFilters} />
        ) : (
          <EmptyState glyph="▢" title="Nothing here yet" hint="Deploys and backups for your teams show up here." />
        )
      }
    >
      {(data) => {
        const visible = data.items.filter((it) => matches(it, filters));
        const more = data.next_before !== "";
        const older = more && onMore !== undefined && limit < INBOX_MAX;
        // Busy only for the page that was asked for — not for every background
        // refetch the events stream triggers. Until the bigger page lands the
        // rows on screen are fewer than the limit; a full inbox smaller than
        // the limit never shows the button in the first place.
        const loadingOlder = page.isFetching && data.items.length < limit;
        const foot = footer?.({ shown: data.items.length, more });
        if (visible.length === 0) {
          // The server page had rows; the client-side narrowing left none.
          return (
            <EmptyState
              glyph="≡"
              title="Nothing matches these filters."
              hint={more ? "Older items were not loaded — clear the filters to see everything." : undefined}
              action={
                <Button size="sm" variant="secondary" onClick={onClearFilters}>
                  Clear filters
                </Button>
              }
            />
          );
        }
        return (
          <>
            <ul ref={rowsRef} className={cn(!wide && "-mx-5")}>
              {visible.map((it) => (
                <InboxRow
                  key={it.id}
                  item={it}
                  layout={layout}
                  projectName={names.get(it.project_id)}
                  onRead={refresh}
                  onNavigate={onNavigate}
                />
              ))}
            </ul>
            {(foot || older) && (
              <div
                className={cn(
                  "flex flex-wrap items-center justify-center gap-x-3 gap-y-2 text-[11.5px] text-text-faint",
                  wide ? "px-4 py-4 sm:px-8" : "pt-3",
                )}
              >
                {foot}
                {older && (
                  <ActionButton
                    size="sm"
                    variant="secondary"
                    state={loadingOlder ? "busy" : "idle"}
                    busyLabel="Loading…"
                    onClick={onMore}
                  >
                    Show older
                  </ActionButton>
                )}
              </div>
            )}
          </>
        );
      }}
    </PageState>
  );
}

function NothingUnread({ onClear }: { onClear: () => void }) {
  return (
    <EmptyState
      glyph="≡"
      title="Nothing unread."
      action={
        <Button size="sm" variant="secondary" onClick={onClear}>
          Show all
        </Button>
      }
    />
  );
}

/** Same geometry as the design's pills (14e: 9px 14px, 11.5px/600), on the Link and the Button alike. */
const PILL = "inline-flex h-8 items-center justify-center whitespace-nowrap rounded-full px-3.5 text-[11.5px] font-semibold";

function InboxRow({
  item,
  layout,
  projectName,
  onRead,
  onNavigate,
}: {
  item: InboxItem;
  layout: "panel" | "page";
  projectName: string | undefined;
  onRead: () => void;
  onNavigate?: () => void;
}) {
  const markRead = useMarkInboxItemRead({ mutation: { onSuccess: onRead } });
  const isUnread = item.read_at == null;
  const isError = item.severity === "error";
  const wide = layout === "page";
  const linkLabel = item.link_label === "" ? "Open" : item.link_label;
  const parkedDep = parkedDeploymentId(item);

  const follow = () => {
    // Reading is explicit: following the link is the act that marks it,
    // which is why opening the panel marks nothing.
    if (isUnread) markRead.mutate({ id: item.id });
    onNavigate?.();
  };

  // Deciding is a stronger act than reading, so it marks the row too: a bell
  // still counting a deploy this person has just approved is counting nothing.
  const decided = () => {
    if (isUnread) markRead.mutate({ id: item.id });
    else onRead();
  };

  return (
    <li
      data-row
      className={cn(
        "border-b border-border-subtle",
        wide ? "px-4 py-3.5 sm:px-8" : "px-5 py-3",
        // Unread rows are tinted one step up from their surface (13u/14e):
        // the page lifts to `surface`, the panel — already on surface — to
        // `raised`. Weight and colour on the title say it a second way.
        isUnread && (wide ? "bg-surface" : "bg-raised"),
      )}
    >
      <div className={cn("flex items-start", wide ? "gap-[9px]" : "gap-[11px]")}>
        <span
          role="img"
          aria-label={isError ? "error" : "ok"}
          className={cn(
            "mt-[5px] h-2 w-2 flex-none",
            isError ? "rounded-[2px] bg-status-error" : "rounded-full bg-status-running",
          )}
        />
        <div className="min-w-0 flex-1">
          <p
            className={cn(
              "text-[12.5px] leading-[1.45]",
              isUnread ? "font-medium text-text" : "text-text-dim",
            )}
          >
            {isUnread && <span className="sr-only">Unread: </span>}
            {item.title}
          </p>
          {item.body !== "" && (
            // Rendered as text. A notification body is machine-assembled from
            // deploy output, and there is no version of this that gets to be
            // markup.
            <p className="mt-0.5 line-clamp-3 whitespace-pre-line text-[12px] leading-[1.5] text-text-faint">
              {item.body}
            </p>
          )}
          <p
            className={cn(
              "mt-[3px] flex flex-wrap items-center gap-x-1.5 font-mono text-text-faint",
              wide ? "text-[10px]" : "text-[10.5px]",
            )}
          >
            {projectName && (
              <>
                <span>{projectName}</span>
                <span aria-hidden>·</span>
              </>
            )}
            <span title={absoluteTime(item.created_at)}>{whenLabel(item)}</span>
            {item.digest && (
              <>
                <span aria-hidden>·</span>
                <span>digest</span>
              </>
            )}
            {/* On the page the link is normally the ink pill below. An approval
                row spends both its pills on the decision, so the link stays
                here — approving a deployment you cannot open first is exactly
                the "vouch for a number" the protection screen refuses to ask
                for — and it keeps Enter on the row opening rather than
                deciding. */}
            {(!wide || parkedDep !== undefined) && item.link !== "" && (
              <>
                <span aria-hidden>·</span>
                <Link
                  to={item.link}
                  onClick={follow}
                  data-row-open
                  className="font-sans text-[11.5px] font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
                >
                  {linkLabel} →
                </Link>
              </>
            )}
            {!wide && item.link === "" && isUnread && (
              <>
                <span aria-hidden>·</span>
                <button
                  type="button"
                  onClick={() => markRead.mutate({ id: item.id })}
                  disabled={markRead.isPending}
                  className="font-sans text-[11.5px] font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
                >
                  Mark read
                </button>
              </>
            )}
          </p>
        </div>
      </div>
      {/* 14e: the actions are pills indented under the title, the ink one
          first. The API gives an item one link and no verbs (an item links,
          it never acts — notification-inbox.md), so on most rows the ink pill
          is that link and the outline pill is the only other thing a row can
          do. A read row with nothing to open, the digest, has no pills at all.
          The one row that carries real verbs is the parked deploy, which is
          14e's own example: approving from a phone must not cost a trip
          through Projects → Settings → Protection. */}
      {wide && (item.link !== "" || isUnread) && (
        <div className="mt-2.5 flex flex-wrap gap-2 pl-[17px]">
          {parkedDep !== undefined ? (
            <ApprovalActions depId={parkedDep} onDecided={decided} />
          ) : (
            <>
              {item.link !== "" && (
                <Link
                  to={item.link}
                  onClick={follow}
                  data-row-open
                  className={cn(PILL, "bg-primary text-primary-fg hover:bg-primary-hover hover:shadow-lift")}
                >
                  {linkLabel}
                </Link>
              )}
              {isUnread && (
                <ActionButton
                  size="sm"
                  variant="secondary"
                  state={markRead.isPending ? "busy" : "idle"}
                  busyLabel="Marking…"
                  onClick={() => markRead.mutate({ id: item.id })}
                  className={cn(PILL, "border border-border-input bg-surface hover:bg-raised")}
                >
                  Mark read
                </ActionButton>
              )}
            </>
          )}
        </div>
      )}
    </li>
  );
}

/**
 * 14e's pair on a parked deploy: "Approve & deploy" in ink, "Reject" outlined
 * in the refusal's own colour. It is safe to put the decision on the row
 * because only an approver ever receives this kind — the plane resolves the
 * recipients against the environment's required rank
 * (deploy-protection.md, `ListApprovalInboxRecipients`) — so these pills are
 * never an affordance waiting to be refused.
 *
 * Neither pill is the row's `data-row-open`: Enter walking the list opens the
 * deployment, and a keystroke that ships code is not a shortcut anyone asked
 * for.
 */
function ApprovalActions({ depId, onDecided }: { depId: string; onDecided: () => void }) {
  const approve = useApproveDeployment({
    mutation: {
      onSuccess: () => {
        toastSuccess("Approved — the deploy is on its way");
        onDecided();
      },
      onError: (e: unknown) => toastFailed("Could not approve the deploy", e),
    },
  });
  const reject = useRejectDeployment({
    mutation: {
      onSuccess: () => {
        toastSuccess("Rejected");
        onDecided();
      },
      onError: (e: unknown) => toastFailed("Could not reject the deploy", e),
    },
  });

  return (
    <>
      <ActionButton
        size="sm"
        variant="primary"
        state={approve.isPending ? "busy" : "idle"}
        busyLabel="Approving…"
        successLabel="Approved"
        failedLabel="Retry"
        onClick={() => approve.mutate({ id: depId })}
        className={PILL}
      >
        Approve &amp; deploy
      </ActionButton>
      <RejectPill
        depId={depId}
        busy={reject.isPending}
        onReject={(reason) => reject.mutate({ id: depId, data: { reason } })}
      />
    </>
  );
}

/**
 * A rejection carries a sentence — the requester reads it and it is
 * audit-logged — and the API requires one, so the pill opens a dialog instead
 * of acting. The same question the protection screen asks, asked from a row.
 */
function RejectPill({ depId, onReject, busy }: { depId: string; onReject: (reason: string) => void; busy: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          className={cn(PILL, "border border-border-input bg-surface text-danger hover:bg-raised")}
        >
          Reject
        </Button>
      </DialogTrigger>
      <DialogContent title="Reject this deploy?" description="Your reason reaches whoever asked for it, and the decision is audit-logged.">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            onReject(String(f.get("reason") ?? "").trim());
            setOpen(false);
          }}
          className="space-y-3"
        >
          <Field label="Reason" qualifier="· what the requester will read">
            {(id) => <Input id={id} name="reason" required maxLength={500} placeholder="Frozen until the incident is closed." autoFocus />}
          </Field>
          <p className="font-mono text-[11px] text-text-faint">deployment {depId}</p>
          <div className="flex justify-end gap-2.5">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton type="submit" variant="danger" size="lg" state={busy ? "busy" : "idle"} busyLabel="Rejecting…">
              Reject deploy
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
