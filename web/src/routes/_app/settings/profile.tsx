// Settings · Profile — canvas 13i (the dark render of 7a): who you are, how the
// panel looks to you, and a way through to the credentials that protect it.
//
// 13i is tagged PROPOSED in the canvas, and most of what it draws has no API:
// `User` is `{id, email, role}`, and the only user mutation in openapi.yaml is
// an admin changing someone else's panel role. So the layout is the canvas's,
// but a control is only interactive here when something on the server can
// actually answer it — display name, photo, timezone and the digest chips are
// shown as the canvas draws them and say plainly that they are not editable
// yet. A control that looks live and does nothing is the dead end
// ui-principles §11 rules out, and shipping one would break CLAUDE.md rule 4
// (API first, always).
//
// What is real: the identity line, the theme preference (client-side, so it
// never needed a server), and the two security rows, which carry live state and
// hand off to the Account tab rather than duplicating its controls.
import { createFileRoute, Link } from "@tanstack/react-router";
import { useGetMe, useGetTotpStatus, useListSessions } from "@/api/gen/auth/auth";
import { Eyebrow } from "@/components/eyebrow";
import { StatusPill } from "@/components/status-badge";
import { Tooltip } from "@/components/ui/tooltip";
import { useCrumbs } from "@/lib/crumbs";
import { setTheme, useThemePreference, type ThemePreference } from "@/lib/theme";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/profile")({ component: ProfileTab });

/** Why a control is inert, said once, in the words of the thing that is missing. */
const NO_API = "Not editable yet — the panel has no API for this field";

/** Initials for the avatar, from the address (there is no name to take them from). */
function initials(email: string): string {
  const local = email.split("@")[0] ?? "";
  const parts = local.split(/[.\-_+]/).filter(Boolean);
  const first = parts[0]?.[0] ?? local[0] ?? "·";
  const second = parts.length > 1 ? (parts[1]?.[0] ?? "") : (local[1] ?? "");
  return (first + second).toUpperCase();
}

const THEMES: { value: ThemePreference; label: string }[] = [
  { value: "light", label: "☀ Light" },
  { value: "dark", label: "☾ Dark" },
  { value: "auto", label: "Auto" },
];

function ProfileTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "account", to: "/settings" }, { label: "profile" }]);
  const me = useGetMe();
  const email = me.data?.email ?? "…";

  return (
    <div className="max-w-2xl space-y-4">
      <IdentityRow email={email} role={me.data?.role} teams={me.data?.teams?.length ?? 0} />

      <div className="grid gap-3 sm:grid-cols-2">
        <ReadOnlyField label="Display name" note={NO_API} value="—" />
        <ReadOnlyField label="Email" qualifier="· sign-in address" value={email} mono />
        {/* Honest rather than aspirational: every absolute timestamp the panel
            prints is UTC (ui-principles §10), so that is what this says until a
            stored preference exists to change it. */}
        <ReadOnlyField label="Timezone" qualifier="· all timestamps" value="UTC" note={NO_API} />
        <ThemeField />
      </div>

      <SecurityRows />

      <section className="space-y-2 pt-1">
        <Eyebrow>Notify me about</Eyebrow>
        <NotifyChips />
        <p className="text-[12px] leading-relaxed text-text-faint">
          Personal email digests — separate from project notifiers, which post to a channel. Project notifiers work
          today; per-person digests need a backend before this can be switched on.
        </p>
      </section>
    </div>
  );
}

function IdentityRow({ email, role, teams }: { email: string; role?: string; teams: number }) {
  return (
    <div className="flex items-center gap-4">
      <span
        aria-hidden
        className="flex size-14 flex-none items-center justify-center rounded-full bg-primary font-mono text-[18px] text-primary-fg"
      >
        {initials(email)}
      </span>
      <div className="min-w-0 flex-1">
        {/* The address is the heading because it is the only name the panel
            knows; 13i's display name sits under it, empty, until there is one. */}
        <p className="truncate text-[20px] font-bold tracking-[-0.02em] text-text">{email}</p>
        <p className="mono mt-0.5 truncate text-[12px] text-text-faint">
          panel {role ?? "…"}
          {teams > 0 && ` · ${teams} team${teams === 1 ? "" : "s"}`}
        </p>
      </div>
      <Tooltip content="Avatars need somewhere to store an image — not built yet">
        <span className="inline-flex">
          <button
            type="button"
            disabled
            className="shrink-0 whitespace-nowrap rounded-full border border-border-input bg-surface px-3.5 py-1.5 text-[12px] font-semibold text-text-disabled"
          >
            Change photo
          </button>
        </span>
      </Tooltip>
    </div>
  );
}

function ReadOnlyField({
  label,
  qualifier,
  value,
  mono,
  note,
}: {
  label: string;
  qualifier?: string;
  value: string;
  mono?: boolean;
  note?: string;
}) {
  return (
    <div>
      <p className="mb-[5px] text-[12px] font-semibold text-text">
        {label}
        {qualifier && <span className="font-normal text-text-faint"> {qualifier}</span>}
      </p>
      <div
        className={cn(
          "truncate rounded-md border border-border-input bg-surface px-[11px] py-[9px] text-[13px]",
          note ? "text-text-disabled" : "text-text",
          mono && "font-mono text-[12.5px]",
        )}
        title={value}
      >
        {value}
      </div>
      {note && <p className="mt-1 text-[11.5px] leading-relaxed text-text-faint">{note}</p>}
    </div>
  );
}

function ThemeField() {
  const preference = useThemePreference();
  return (
    <div>
      <p className="mb-[5px] text-[12px] font-semibold text-text">Theme</p>
      <div
        role="radiogroup"
        aria-label="Theme"
        className="flex overflow-hidden rounded-md border border-border-input bg-surface text-center text-[12.5px] font-semibold"
      >
        {THEMES.map((t) => {
          const active = preference === t.value;
          return (
            <button
              key={t.value}
              type="button"
              role="radio"
              aria-checked={active}
              onClick={() => setTheme(t.value)}
              className={cn(
                "flex-1 py-[9px] transition-colors",
                active ? "bg-primary text-primary-fg" : "text-text-mid hover:text-text",
              )}
            >
              {t.label}
            </button>
          );
        })}
      </div>
      <p className="mt-1 text-[11.5px] leading-relaxed text-text-faint">
        Auto follows your system, and changes with it.
      </p>
    </div>
  );
}

function SecurityRows() {
  const totp = useGetTotpStatus();
  const sessions = useListSessions();
  const enabled = totp.data?.enabled ?? false;
  const left = totp.data?.recovery_codes_left ?? 0;
  const live = sessions.data?.length ?? 0;

  return (
    <div className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
      <Row
        label="Password"
        detail="Set when the account was created"
        action={
          <Tooltip content="Changing a password needs an API route the panel does not have yet">
            <span className="inline-flex">
              <button type="button" disabled className="shrink-0 text-[12px] font-medium text-text-disabled">
                Change
              </button>
            </span>
          </Tooltip>
        }
      >
        <span className="text-[13px] font-semibold text-text">Password</span>
      </Row>

      <Row
        label="Two-factor"
        detail={enabled ? `TOTP · ${left} recovery ${left === 1 ? "code" : "codes"} left` : "A code is not required at sign-in."}
        action={
          <Link to="/settings" className="shrink-0 text-[12px] font-medium text-text-dim hover:underline">
            Manage
          </Link>
        }
      >
        <span className="text-[13px] font-semibold text-text">Two-factor</span>{" "}
        <StatusPill status={enabled ? "running" : "stopped"}>{enabled ? "on" : "off"}</StatusPill>
      </Row>

      <Row
        label="Sessions"
        detail={live === 1 ? "1 live sign-in" : `${live} live sign-ins`}
        action={
          <Link to="/settings" className="shrink-0 text-[12px] font-medium text-text-dim hover:underline">
            Review
          </Link>
        }
      >
        <span className="text-[13px] font-semibold text-text">Sessions</span>
      </Row>
    </div>
  );
}

function Row({
  label,
  detail,
  action,
  children,
}: {
  label: string;
  detail: string;
  action: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-3 px-4 py-3" aria-label={label}>
      <div className="min-w-0 flex-1">
        {children}
        <p className="mt-0.5 text-[11.5px] text-text-faint">{detail}</p>
      </div>
      {action}
    </div>
  );
}

// 13i shows these pre-selected; here none can be, because nothing stores the
// answer. They are drawn in the canvas's unselected state and say why.
const DIGESTS = ["deploy failed", "backup failed", "deploy succeeded", "new team member", "agent degraded"];

function NotifyChips() {
  return (
    <Tooltip content="Personal digests need a backend — no API stores these yet">
      <div className="flex flex-wrap gap-[7px]">
        {DIGESTS.map((d) => (
          <span
            key={d}
            className="rounded-full border border-border bg-surface px-3 py-1 text-[12px] text-text-disabled"
          >
            {d}
          </span>
        ))}
      </div>
    </Tooltip>
  );
}
