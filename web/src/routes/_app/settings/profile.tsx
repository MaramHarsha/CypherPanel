// Settings · Profile — canvas 13i (the dark render of 7a): who you are, how the
// panel looks to you, and the credentials that protect it.
//
// The fields here were UI-only until the API caught up; they are real now
// (PATCH /auth/me, POST /auth/password). What is still drawn but inert is the
// avatar and the personal digest chips, because nothing stores an image or a
// per-person subscription yet — and a control that looks live and does nothing
// is the dead end ui-principles §11 rules out.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getGetMeQueryKey,
  getListSessionsQueryKey,
  useChangePassword,
  useGetMe,
  useGetTotpStatus,
  useListSessions,
  useUpdateProfile,
} from "@/api/gen/auth/auth";
import { ApiError } from "@/api/client";
import { Eyebrow } from "@/components/eyebrow";
import { StatusPill } from "@/components/status-badge";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { Tooltip } from "@/components/ui/tooltip";
import { useCrumbs } from "@/lib/crumbs";
import { setTheme, useThemePreference, type ThemePreference } from "@/lib/theme";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/profile")({ component: ProfileTab });

/** Initials for the avatar — from the name once there is one, else the address. */
function initials(name: string, email: string): string {
  const source = name.trim() || (email.split("@")[0] ?? "");
  const parts = source.split(/[\s.\-_+]/).filter(Boolean);
  const first = parts[0]?.[0] ?? source[0] ?? "·";
  const second = parts.length > 1 ? (parts[1]?.[0] ?? "") : (source[1] ?? "");
  return (first + second).toUpperCase();
}

/**
 * The zones this browser knows. `supportedValuesOf` is the only way to get the
 * real list without shipping one, and a browser too old to have it still gets a
 * working field — it just types the name instead of picking it.
 */
function zones(): string[] | null {
  try {
    const supported = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    return supported ? ["UTC", ...supported("timeZone").filter((z) => z !== "UTC")] : null;
  } catch {
    return null;
  }
}

const THEMES: { value: ThemePreference; label: string }[] = [
  { value: "light", label: "☀ Light" },
  { value: "dark", label: "☾ Dark" },
  { value: "auto", label: "Auto" },
];

function ProfileTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "account", to: "/settings" }, { label: "profile" }]);
  const me = useGetMe();
  const email = me.data?.email ?? "";
  const name = me.data?.display_name ?? "";

  return (
    <div className="max-w-2xl space-y-4">
      <div className="flex items-center gap-4">
        <span
          aria-hidden
          className="flex size-14 flex-none items-center justify-center rounded-full bg-primary font-mono text-[18px] text-primary-fg"
        >
          {email ? initials(name, email) : "·"}
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-[20px] font-bold tracking-[-0.02em] text-text">{name || email || "…"}</p>
          <p className="mono mt-0.5 truncate text-[12px] text-text-faint">
            {name ? `${email} · ` : ""}panel {me.data?.role ?? "…"}
            {(me.data?.teams?.length ?? 0) > 0 && ` · ${me.data?.teams.length} team${me.data?.teams.length === 1 ? "" : "s"}`}
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

      <ProfileForm email={email} name={name} timezone={me.data?.timezone ?? ""} />
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

function ProfileForm({ email, name, timezone }: { email: string; name: string; timezone: string }) {
  const qc = useQueryClient();
  const [draftName, setDraftName] = useState<string | null>(null);
  const [draftZone, setDraftZone] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const tzList = zones();

  // `null` means "not touched", so the fields keep following the server until
  // the operator actually edits one — otherwise a save elsewhere would be
  // silently overwritten by a stale draft.
  const nextName = draftName ?? name;
  const nextZone = draftZone ?? timezone;
  const dirty = nextName !== name || nextZone !== timezone;

  const save = useUpdateProfile({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetMeQueryKey() });
        setDraftName(null);
        setDraftZone(null);
        toast.success("Profile saved");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save your profile"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({ data: { display_name: nextName, timezone: nextZone } });
  };

  return (
    <form onSubmit={submit}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Display name" qualifier="· what teammates see">
          {(id) => (
            <Input
              id={id}
              className="font-sans"
              maxLength={64}
              placeholder={email.split("@")[0] ?? ""}
              value={nextName}
              onChange={(e) => setDraftName(e.target.value)}
            />
          )}
        </Field>

        <Field label="Email" qualifier="· sign-in address">
          {(id) => (
            <Tooltip content="Changing the sign-in address needs a re-verification flow the panel does not have yet">
              <span className="inline-flex w-full">
                <Input id={id} value={email} readOnly disabled />
              </span>
            </Tooltip>
          )}
        </Field>

        <Field label="Timezone" qualifier="· all timestamps">
          {(id) =>
            tzList ? (
              <Select
                id={id}
                className="font-sans"
                value={nextZone || "UTC"}
                onChange={(e) => setDraftZone(e.target.value === "UTC" ? "" : e.target.value)}
              >
                {tzList.map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </Select>
            ) : (
              <Input
                id={id}
                className="font-sans"
                placeholder="UTC"
                value={nextZone}
                onChange={(e) => setDraftZone(e.target.value)}
              />
            )
          }
        </Field>

        <ThemeField />
      </div>

      {error && (
        <p role="alert" className="mt-3 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {error}
        </p>
      )}

      {/* The save only appears once something has changed: a button that does
          nothing most of the time teaches people to ignore it. */}
      {dirty && (
        <div className="mt-3 flex items-center gap-3">
          <ActionButton type="submit" variant="primary" state={save.isPending ? "busy" : "idle"} busyLabel="Saving…">
            Save changes
          </ActionButton>
          <button
            type="button"
            onClick={() => {
              setDraftName(null);
              setDraftZone(null);
              setError(null);
            }}
            className="text-[12.5px] text-text-mid hover:text-text"
          >
            Discard
          </button>
        </div>
      )}
    </form>
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
      <p className="mt-1 text-[11.5px] leading-relaxed text-text-faint">Auto follows your system, and changes with it.</p>
    </div>
  );
}

function SecurityRows() {
  const totp = useGetTotpStatus();
  const sessions = useListSessions();
  const enabled = totp.data?.enabled ?? false;
  const left = totp.data?.recovery_codes_left ?? 0;
  const live = sessions.data?.length ?? 0;
  const others = (sessions.data ?? []).filter((s) => !s.current).length;

  return (
    <div className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
      <Row title="Password" detail="Rotate it, and decide what happens to your other devices">
        <ChangePasswordDialog others={others} />
      </Row>

      <Row
        title="Two-factor"
        chip={<StatusPill status={enabled ? "running" : "stopped"}>{enabled ? "on" : "off"}</StatusPill>}
        detail={enabled ? `TOTP · ${left} recovery ${left === 1 ? "code" : "codes"} left` : "A code is not required at sign-in."}
      >
        <Link to="/settings" className="shrink-0 text-[12px] font-medium text-text-dim hover:underline">
          Manage
        </Link>
      </Row>

      <Row title="Sessions" detail={live === 1 ? "1 live sign-in" : `${live} live sign-ins`}>
        <Link to="/settings" className="shrink-0 text-[12px] font-medium text-text-dim hover:underline">
          Review
        </Link>
      </Row>
    </div>
  );
}

function Row({
  title,
  chip,
  detail,
  children,
}: {
  title: string;
  chip?: React.ReactNode;
  detail: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <span className="text-[13px] font-semibold text-text">{title}</span> {chip}
        <p className="mt-0.5 text-[11.5px] text-text-faint">{detail}</p>
      </div>
      {children}
    </div>
  );
}

/** A hint, not a gate — the server's only rule is the eight-character floor. */
function strength(pw: string): number {
  if (!pw) return 0;
  let score = pw.length >= 8 ? 1 : 0;
  if (pw.length >= 12) score++;
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw) && /\d/.test(pw)) score++;
  if (/[^\w\s]/.test(pw)) score++;
  return score;
}

function ChangePasswordDialog({ others }: { others: number }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [revoke, setRevoke] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const change = useChangePassword({
    mutation: {
      onSuccess: (res) => {
        const n = (res as { revoked?: number }).revoked ?? 0;
        toast.success(n > 0 ? `Password changed · ${n} other session${n === 1 ? "" : "s"} signed out` : "Password changed");
        void qc.invalidateQueries({ queryKey: getListSessionsQueryKey() });
        setOpen(false);
      },
      onError: (e: unknown) =>
        setError(e instanceof ApiError ? e.message : e instanceof Error ? e.message : "Could not change your password"),
    },
  });

  const score = strength(next);

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setCurrent("");
          setNext("");
          setRevoke(true);
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <button type="button" className="shrink-0 text-[12px] font-medium text-text-dim hover:underline">
          Change
        </button>
      </DialogTrigger>
      <DialogContent title="Change password">
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            setError(null);
            change.mutate({ data: { current_password: current, new_password: next, revoke_other_sessions: revoke } });
          }}
          className="space-y-3"
        >
          <Field label="Current password">
            {(id) => (
              <Input
                id={id}
                type="password"
                autoComplete="current-password"
                required
                autoFocus
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            )}
          </Field>

          <div>
            <Field label="New password" hint="At least 8 characters.">
              {(id) => (
                <Input
                  id={id}
                  type="password"
                  autoComplete="new-password"
                  required
                  value={next}
                  onChange={(e) => setNext(e.target.value)}
                />
              )}
            </Field>
            {/* Four segments that fill as the password earns them (canvas 9i).
                Green is the running colour: this is a health readout, not a
                status the server will enforce. */}
            <div className="mt-1.5 flex gap-1" aria-hidden>
              {[1, 2, 3, 4].map((i) => (
                <span
                  key={i}
                  className={cn("h-1 flex-1 rounded-full", i <= score ? "bg-status-running" : "bg-border-subtle")}
                />
              ))}
            </div>
          </div>

          {/* The session question, asked here rather than assumed — it is the
              reason most people are on this screen. */}
          <label className="flex items-center gap-2.5 rounded-lg border border-border bg-surface px-3.5 py-3 text-[12.5px] leading-snug text-text-dim">
            <input
              type="checkbox"
              checked={revoke}
              onChange={(e) => setRevoke(e.target.checked)}
              className="size-4 shrink-0 accent-[var(--primary)]"
            />
            <span>
              Also sign out my{" "}
              <b className="text-text">
                {others} other session{others === 1 ? "" : "s"}
              </b>{" "}
              — the reason most people are on this screen.
            </span>
          </label>

          {error && (
            <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
              {error}
            </p>
          )}

          <div className="flex justify-end gap-2.5 pt-1">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="accent"
              state={change.isPending ? "busy" : "idle"}
              busyLabel="Changing…"
              disabled={current === "" || next === ""}
            >
              Change password
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// 13i shows these pre-selected; nothing stores the answer yet, so they are drawn
// in the canvas's unselected state and say why.
const DIGESTS = ["deploy failed", "backup failed", "deploy succeeded", "new team member", "agent degraded"];

function NotifyChips() {
  return (
    <Tooltip content="Personal digests need a backend — no API stores these yet">
      <div className="flex flex-wrap gap-[7px]">
        {DIGESTS.map((d) => (
          <span key={d} className="rounded-full border border-border bg-surface px-3 py-1 text-[12px] text-text-disabled">
            {d}
          </span>
        ))}
      </div>
    </Tooltip>
  );
}
