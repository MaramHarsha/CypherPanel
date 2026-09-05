// The bell and the inbox panel (canvas 13u, notification-inbox.md §7).
//
// It is CHROME, not navigation: ui-principles §4 fixes the top-level nav at
// exactly four items, and the bell is not a fifth — it opens a panel in place
// and leads nowhere. That is also why it lives in the top bar's control cluster
// rather than in the nav strip beside Projects and Servers. The full page the
// phone's bottom bar leads to (canvas 14e) is routes/_app/inbox.tsx; the panel
// links there for anyone who wants the feed with room around it.
//
// The rows and their rules live in inbox-list.tsx, shared with that page.
import { Link } from "@tanstack/react-router";
import { Bell } from "lucide-react";
import { useState } from "react";
import { useGetInboxUnreadCount } from "@/api/gen/inbox/inbox";
import { badgeLabel, CountPill, InboxList, MarkAllRead, NO_FILTERS } from "@/components/inbox-list";
import { Drawer } from "@/components/ui/drawer";
import { cn } from "@/lib/utils";

/** The page the panel asks for; §6 caps the server side at 100 regardless. */
const PAGE_SIZE = 20;

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
        {/* Exact to 99, then "99+". Zero renders NO badge — never a "0",
            which would make an empty inbox look like a thing that had
            happened. */}
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
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      label="Inbox"
      title={
        <span className="flex items-center gap-2">
          Inbox
          <CountPill count={unread} />
        </span>
      }
    >
      {/* The toolbar sits below the drawer's own head rather than inside it:
          the head is a heading element, and a heading is no place for a
          control — so "Mark all read" rides this line's right edge instead
          of the head's, the one departure from 13u the markup forces. */}
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
        <MarkAllRead unread={unread} />
      </div>

      <div className="px-5 pb-3">
        <InboxList
          filters={{ ...NO_FILTERS, unreadOnly }}
          limit={PAGE_SIZE}
          layout="panel"
          onClearFilters={() => onUnreadOnly(false)}
          onNavigate={() => onOpenChange(false)}
          footer={({ more }) => (
            <span>
              {more ? `Showing the ${PAGE_SIZE} most recent · ` : ""}
              <Link
                to="/inbox"
                onClick={() => onOpenChange(false)}
                className="font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
              >
                Open inbox →
              </Link>
            </span>
          )}
        />
      </div>

      {/* 13u's footnote, with the preferences it names one tap away. */}
      <p className="border-t border-border px-5 py-3 text-[11.5px] leading-[1.5] text-text-faint">
        Only teams you belong to. Success is digested, failure is immediate.{" "}
        <Link
          to="/settings/profile"
          onClick={() => onOpenChange(false)}
          className="font-medium text-text-mid underline-offset-2 hover:text-text hover:underline"
        >
          Preferences →
        </Link>
      </p>
    </Drawer>
  );
}
