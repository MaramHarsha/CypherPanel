// The bell and the inbox panel (canvas 13u, notification-inbox.md §7).
//
// It is CHROME, not navigation: ui-principles §4 fixes the top-level nav at
// exactly four items, and the bell is not a fifth — it opens a panel in place
// and leads nowhere. That is also why it lives in the top bar's control cluster
// rather than in the nav strip beside Projects and Servers.
//
// Three decisions the panel is built around:
//
//   · reading is EXPLICIT. Opening the panel marks nothing; clicking an item's
//     link marks that one and navigates. Auto-clearing on open would destroy
//     the only signal the bell carries, and would make "Mark all read"
//     meaningless;
//   · a digest is one row and one unread however many events it holds, and it
//     carries no action link — a rollup of three backups has no single thing to
//     open;
//   · the marks are the item's, not the row's: severity `error` takes the
//     square marker from StatusDot's shape language rather than inventing a
//     second status vocabulary (ui-principles §5).
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Bell } from "lucide-react";
import { useState } from "react";
import {
  getGetInboxUnreadCountQueryKey,
  getListInboxQueryKey,
  useGetInboxUnreadCount,
  useListInbox,
  useMarkAllInboxRead,
  useMarkInboxItemRead,
} from "@/api/gen/inbox/inbox";
import type { InboxItem } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Drawer } from "@/components/ui/drawer";
import { absoluteTime, relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

/** The page the panel asks for; §6 caps the server side at 100 regardless. */
const PAGE_SIZE = 20;

/**
 * The badge is exact to 99 and then says so rather than lying by rounding. Zero
 * renders NO badge — never a "0", which would make an empty inbox look like a
 * thing that had happened.
 */
function badgeLabel(n: number): string {
  return n > 99 ? "99+" : String(n);
}

export function InboxBell() {
  const [open, setOpen] = useState(false);
  const [unreadOnly, setUnreadOnly] = useState(false);
  const count = useGetInboxUnreadCount();
  const unread = count.data?.unread ?? 0;

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={unread > 0 ? `Inbox, ${unread} unread` : "Inbox, nothing unread"}
        title="Inbox"
        // 34px on `border-border-input`, the same control line as the search
        // circle, the team chip and the theme toggle it sits among.
        className="relative flex h-[34px] w-[34px] shrink-0 items-center justify-center rounded-full border border-border-input bg-surface text-text-mid hover:border-border-strong hover:text-text"
      >
        <Bell className="h-4 w-4" aria-hidden />
        {unread > 0 && (
          <span
            aria-hidden
            className="absolute -right-0.5 -top-0.5 flex h-[17px] min-w-[17px] items-center justify-center rounded-full bg-accent px-1 font-mono text-[10px] font-semibold leading-none text-accent-fg"
          >
            {badgeLabel(unread)}
          </span>
        )}
      </button>
      {/* Mounted only while open: the feed is a burst read, not a background
          subscription, so a closed panel costs nothing. */}
      {open && (
        <InboxPanel
          open={open}
          onOpenChange={setOpen}
          unread={unread}
          unreadOnly={unreadOnly}
          onUnreadOnly={setUnreadOnly}
        />
      )}
    </>
  );
}

function InboxPanel({
  open,
  onOpenChange,
  unread,
  unreadOnly,
  onUnreadOnly,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  unread: number;
  unreadOnly: boolean;
  onUnreadOnly: (v: boolean) => void;
}) {
  const qc = useQueryClient();
  const params = { unread: unreadOnly || undefined, limit: PAGE_SIZE };
  const page = useListInbox(params);

  // A mark changes both the feed and the bell, so both keys go. The list key
  // is taken without params on purpose: TanStack matches key prefixes, so this
  // one invalidation covers the filtered variant as well as the unfiltered one,
  // and the two can never disagree about the count.
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: getGetInboxUnreadCountQueryKey() });
    void qc.invalidateQueries({ queryKey: getListInboxQueryKey() });
  };

  const markAll = useMarkAllInboxRead({ mutation: { onSuccess: refresh } });

  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      label="Inbox"
      title={
        <span className="flex items-baseline gap-2">
          Inbox
          {unread > 0 && <span className="font-mono text-[12px] text-text-faint">{unread}</span>}
        </span>
      }
    >
      {/* The toolbar sits below the drawer's own head rather than inside it:
          the head is a heading element, and a heading is no place for two
          controls. "Mark all read" is `secondary`, not accent — accent marks
          the one unmissable action on a screen, and clearing a count is not
          it. */}
      <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-2.5">
        <button
          type="button"
          onClick={() => onUnreadOnly(!unreadOnly)}
          aria-pressed={unreadOnly}
          className={cn(
            "rounded-full border px-2.5 py-1 text-[11.5px] font-medium",
            unreadOnly
              ? "border-border-strong bg-raised text-text"
              : "border-border-input text-text-mid hover:border-border-strong hover:text-text",
          )}
        >
          Unread only
        </button>
        <Button
          size="sm"
          variant="secondary"
          disabled={unread === 0 || markAll.isPending}
          onClick={() => markAll.mutate()}
        >
          Mark all read
        </Button>
      </div>

      <div className="px-5 py-3">
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
            unreadOnly ? (
              <EmptyState
                glyph="≡"
                title="Nothing unread."
                action={
                  <Button size="sm" variant="secondary" onClick={() => onUnreadOnly(false)}>
                    Show all
                  </Button>
                }
              />
            ) : (
              <EmptyState
                glyph="▢"
                title="Nothing here yet"
                hint="Deploys and backups for your teams show up here."
              />
            )
          }
        >
          {(data) => (
            <ul className="space-y-px">
              {data.items.map((it) => (
                <InboxRow key={it.id} item={it} onRead={refresh} onNavigate={() => onOpenChange(false)} />
              ))}
              {data.next_before !== "" && (
                <li className="pt-3 text-center text-[11.5px] text-text-faint">
                  Showing the {PAGE_SIZE} most recent.
                </li>
              )}
            </ul>
          )}
        </PageState>
      </div>
    </Drawer>
  );
}

function InboxRow({
  item,
  onRead,
  onNavigate,
}: {
  item: InboxItem;
  onRead: () => void;
  onNavigate: () => void;
}) {
  const markRead = useMarkInboxItemRead({ mutation: { onSuccess: onRead } });
  const isUnread = item.read_at == null;
  const isError = item.severity === "error";

  return (
    <li
      className={cn(
        // The unread rule is the ink hairline down the left edge —
        // `border-strong`, the same strongest-line-in-the-product the top bar
        // sits on — rather than a tint, which would fight the severity marker.
        "border-l-2 py-2.5 pl-3",
        isUnread ? "border-border-strong" : "border-transparent",
      )}
    >
      <div className="flex items-start gap-2.5">
        {/* Decorative: the title already carries the meaning, and StatusDot's
            own label speaks the resource vocabulary, which is not this one. */}
        <span aria-hidden className="mt-[5px]">
          <StatusDot status={isError ? "error" : "running"} />
        </span>
        <div className="min-w-0 flex-1">
          <p
            className={cn(
              "text-[13px] leading-snug",
              isUnread ? "font-medium text-text" : "text-text-dim",
              isError && isUnread && "text-danger",
            )}
          >
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
          <p className="mt-1 flex flex-wrap items-center gap-x-2 text-[11.5px] text-text-faint">
            <span title={absoluteTime(item.created_at)}>{relativeTime(item.created_at)}</span>
            {item.digest && <span>· digest</span>}
            {item.link !== "" && (
              <>
                <span aria-hidden>·</span>
                <Link
                  to={item.link}
                  onClick={() => {
                    // Reading is explicit: following the link is the act that
                    // marks it, which is why opening the panel marks nothing.
                    if (isUnread) markRead.mutate({ id: item.id });
                    onNavigate();
                  }}
                  className="font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
                >
                  {item.link_label === "" ? "Open" : item.link_label} →
                </Link>
              </>
            )}
            {item.link === "" && isUnread && (
              <>
                <span aria-hidden>·</span>
                <button
                  type="button"
                  onClick={() => markRead.mutate({ id: item.id })}
                  disabled={markRead.isPending}
                  className="font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
                >
                  Mark read
                </button>
              </>
            )}
          </p>
        </div>
      </div>
    </li>
  );
}
