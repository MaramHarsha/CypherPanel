// /inbox — the notification feed as a page (canvas 14e, "approve from the
// couch"). On a phone it is the third bottom tab and the whole screen; from
// `sm` up it is the same feed with the page's gutters and the masthead's
// title size, so the bell's panel and this page never disagree about a row.
//
// The head is the canvas's, not PageHeader's: "Inbox", the accent count, and
// "Mark all read" as a text action on the right, over a 1.5px ink rule — the
// strongest line on the page, because the list under it is the content and
// there is no dateline above a page that has no parent.
//
// Filters: `Unread only` is the API's own (?unread); kind and severity narrow
// the rows on screen, since the API has no parameter for them. Each is a
// pressed chip, and filtered-to-zero is its own state with the way out in it
// (ui-principles §7). The feed itself — rows, pills, empties, the older-rows
// button — is inbox-list.tsx, shared with the bell.
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { useGetInboxUnreadCount } from "@/api/gen/inbox/inbox";
import {
  CountPill,
  INBOX_MAX,
  InboxList,
  isFiltered,
  MarkAllRead,
  NO_FILTERS,
  type InboxFilters,
  type KindFilter,
} from "@/components/inbox-list";
import { useRowNavigation } from "@/lib/keys";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/inbox")({ component: InboxPage });

/** The first page; "Show older" grows it by one page at a time up to the server's cap. */
const PAGE_SIZE = 20;

const KINDS: { value: KindFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "deploy", label: "Deploys" },
  { value: "backup", label: "Backups" },
];

function InboxPage() {
  const [filters, setFilters] = useState<InboxFilters>(NO_FILTERS);
  const [limit, setLimit] = useState(PAGE_SIZE);
  const unread = useGetInboxUnreadCount().data?.unread ?? 0;
  const rows = useRowNavigation();
  const set = (patch: Partial<InboxFilters>) => setFilters((f) => ({ ...f, ...patch }));
  const clear = () => setFilters(NO_FILTERS);

  return (
    <div>
      <header className="flex items-center gap-2 border-b-[1.5px] border-border-strong px-4 pb-4 pt-4 sm:px-8 sm:pb-[22px] sm:pt-[34px]">
        <h1 className="text-[19px] font-bold leading-none tracking-[-0.02em] sm:text-[34px] sm:tracking-[-0.03em]">
          Inbox
        </h1>
        <CountPill count={unread} className="sm:text-[11px]" />
        <MarkAllRead unread={unread} className="ml-auto sm:text-[12.5px]" />
      </header>

      <div
        role="group"
        aria-label="Filter the inbox"
        className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5 sm:px-8"
      >
        <Chip pressed={filters.unreadOnly} onClick={() => set({ unreadOnly: !filters.unreadOnly })}>
          Unread only
        </Chip>
        <Chip
          pressed={filters.severity === "error"}
          onClick={() => set({ severity: filters.severity === "error" ? "all" : "error" })}
        >
          Errors
        </Chip>
        <span aria-hidden className="mx-1 h-4 w-px bg-border" />
        {KINDS.map((k) => (
          <Chip key={k.value} pressed={filters.kind === k.value} onClick={() => set({ kind: k.value })}>
            {k.label}
          </Chip>
        ))}
        {isFiltered(filters) && (
          <button
            type="button"
            onClick={clear}
            className="ml-auto text-[11.5px] font-medium text-text-faint underline-offset-2 hover:text-text hover:underline"
          >
            Clear
          </button>
        )}
      </div>

      <InboxList
        filters={filters}
        limit={limit}
        layout="page"
        rowsRef={rows}
        onClearFilters={clear}
        onMore={() => setLimit((n) => Math.min(INBOX_MAX, n + PAGE_SIZE))}
        footer={({ shown, more }) =>
          more && shown >= INBOX_MAX ? (
            <span>Showing the {INBOX_MAX} most recent — the feed keeps no more than that.</span>
          ) : more ? (
            <span>Showing the {shown} most recent.</span>
          ) : null
        }
      />

      {/* 13u's footnote, with the preferences it names one tap away. */}
      <p className="px-4 py-4 text-[11.5px] leading-[1.5] text-text-faint sm:px-8">
        Only teams you belong to. Success is digested, failure is immediate.{" "}
        <Link
          to="/settings/profile"
          className="font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
        >
          Preferences →
        </Link>
      </p>
    </div>
  );
}

/** A filter chip: the same pressed/unpressed pill the bell's "Unread only" wears. */
function Chip({ pressed, onClick, children }: { pressed: boolean; onClick: () => void; children: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={pressed}
      className={cn(
        "rounded-full border px-2.5 py-1 text-[11.5px] font-medium",
        pressed
          ? "border-border-strong bg-raised text-text"
          : "border-border-input text-text-mid hover:border-border-strong hover:text-text",
      )}
    >
      {children}
    </button>
  );
}
