// Settings · Profile — canvas 13i (the dark render of 7a): who you are, how the
// panel looks to you, and the credentials that protect it.
//
// Everything drawn here is backed by an endpoint: PATCH /auth/me, POST
// /auth/password, PUT /auth/me/avatar, POST /auth/email/change + /confirm, and
// GET/PUT /inbox/preferences behind the chip row. That last one stores MUTES
// rather than subscriptions — a chip that is off is a kind this account has
// silenced, and a kind the plane learns to emit later is on for everyone until
// they say otherwise. It gates the in-panel inbox; personal email digests (the
// canvas's other half) reuse this row when they ship.
import { useMutation, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useId, useRef, useState, type DragEvent, type FormEvent, type ReactNode } from "react";
import {
  getDeleteAvatarUrl,
  getGetMeQueryKey,
  getListSessionsQueryKey,
  getSetAvatarUrl,
  getGetPendingEmailChangeQueryKey,
  useCancelEmailChange,
  useChangePassword,
  useConfirmEmailChange,
  useGetPendingEmailChange,
  useRequestEmailChange,
  useGetMe,
  useGetTotpStatus,
  useListSessions,
  useUpdateProfile,
} from "@/api/gen/auth/auth";
import { ApiError, apiFetch } from "@/api/client";
import { getGetInboxPreferencesQueryKey, useGetInboxPreferences, useSetInboxPreferences } from "@/api/gen/inbox/inbox";
import type { InboxPreferences, Me, MeRole, SetInboxPreferencesRequestMutedKindsItem } from "@/api/gen/model";
import { useGetPanelMail } from "@/api/gen/panel/panel";
import { ChoiceChip } from "@/components/connection-dialog";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { UserAvatar, avatarQueryKey, useAvatar } from "@/components/user-avatar";
import { StatusPill } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { Skeleton, useSkeletonDelay } from "@/components/ui/skeleton";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast } from "@/lib/roles";
import { setTheme, useThemePreference, type ThemePreference } from "@/lib/theme";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

interface ProfileSearch {
  /** The email-change token, carried back in by the mailed confirmation link. */
  confirm?: string;
}

export const Route = createFileRoute("/_app/settings/profile")({
  validateSearch: (s: Record<string, unknown>): ProfileSearch =>
    typeof s.confirm === "string" && s.confirm !== "" ? { confirm: s.confirm } : {},
  component: ProfileTab,
});

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
  const { confirm } = Route.useSearch();
  // The header and the form take their shape from the account, so the page
  // holds that shape while the answer is on its way (canvas 10e, past 200 ms)
  // rather than painting "…" into a name slot as if it were a name.
  const showSkeleton = useSkeletonDelay(me.isPending);

  return (
    <div className="max-w-2xl space-y-4">
      {confirm && <ConfirmEmailCard token={confirm} current={me.data?.email ?? ""} />}
      {/* A move nobody has confirmed yet. It is on the page rather than behind
          the mailed link because the person who needs to see it most is the one
          who did NOT ask for it — they never got the link. */}
      <PendingEmailChangeCard />
      <PageState query={me} loading={showSkeleton ? <ProfileSkeleton /> : null}>
        {(who) => (
          <>
            <ProfileHeader who={who} />
            <ProfileForm who={who} />
          </>
        )}
      </PageState>
      <SecurityRows />
      <section className="pt-1">
        <Eyebrow className="mb-2">Notify me about</Eyebrow>
        <NotifyChips />
        <p className="mt-2.5 text-[12px] leading-relaxed text-text-faint">
          What reaches your inbox in the panel — separate from project notifiers, which post to channels.
        </p>
      </section>
    </div>
  );
}

/** The header and the 2×2 form, as bars — what the page is about to become. */
function ProfileSkeleton() {
  return (
    <div aria-busy className="space-y-4">
      <div className="flex items-center gap-4">
        <Skeleton className="size-14 rounded-full" />
        <div className="min-w-0 flex-1">
          <Skeleton className="h-5 w-40 max-w-full" />
          <Skeleton className="mt-2 h-[11px] w-64 max-w-full bg-border-subtle" />
        </div>
        <Skeleton className="h-7 w-28 rounded-full bg-border-subtle" />
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {[0, 1, 2, 3].map((i) => (
          <div key={i}>
            <Skeleton className="h-3 w-24" />
            <Skeleton className="mt-2 h-9 w-full bg-border-subtle" />
          </div>
        ))}
      </div>
    </div>
  );
}

function ProfileHeader({ who }: { who: Me }) {
  const name = who.display_name ?? "";
  const teams = who.teams.length;
  return (
    <div className="flex items-center gap-4">
      <UserAvatar userId={who.id} name={name} email={who.email} className="size-14" textClassName="text-[18px]" />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[20px] font-bold tracking-[-0.02em] text-text">{name || who.email}</p>
        {/* 13i ends this line "· joined mar 2026"; Me carries no created_at,
            so the team count stands in until the API can say when. */}
        <p className="mono mt-0.5 truncate text-[12px] text-text-faint">
          {name ? `${who.email} · ` : ""}panel {who.role}
          {teams > 0 && ` · ${teams} team${teams === 1 ? "" : "s"}`}
        </p>
      </div>
      <ChangePhotoDialog userId={who.id} name={name} email={who.email} />
    </div>
  );
}

// ─── photo ──────────────────────────────────────────────────────────────────

/** Accepted here as well as on the server, so a wrong file fails before upload. */
const AVATAR_TYPES = ["image/png", "image/jpeg", "image/webp"];
const MAX_AVATAR_BYTES = 2 * 1024 * 1024;

/** A data: URL, because the panel's CSP refuses blob: images (user-avatar.tsx). */
function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

/**
 * Canvas 12e's CHANGE PHOTO sheet: a dashed drop zone with the picture as it
 * will look, one line of limits, and the two footer actions. The picked file
 * previews before anything is sent, so a wrong crop costs a second pick, not
 * an upload and a removal.
 */
function ChangePhotoDialog({ userId, name, email }: { userId: string; name: string; email: string }) {
  const qc = useQueryClient();
  const picker = useRef<HTMLInputElement>(null);
  const hintId = useId();
  const [open, setOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Whether there is a photo to remove comes from the same cached query the
  // picture itself reads, so the controls and the image can never disagree.
  const { data: photo } = useAvatar(userId);

  // One key, two consumers: the profile header and the top-bar chip both read
  // it, so invalidating here changes the picture in both without a reload.
  const done = (msg: string) => {
    void qc.invalidateQueries({ queryKey: avatarQueryKey(userId) });
    void qc.invalidateQueries({ queryKey: getGetMeQueryKey() });
    toastSuccess(msg);
    setOpen(false);
  };

  // The generated client cannot express this one: orval JSON.stringifies the
  // body for a binary request, which would upload "{}". It still owns the URL,
  // and apiFetch still owns auth and the error envelope.
  const upload = useMutation({
    mutationFn: (f: File) =>
      apiFetch<{ etag: string }>(getSetAvatarUrl(), {
        method: "PUT",
        headers: { "Content-Type": f.type },
        body: f,
      }),
    onSuccess: () => done("Photo updated"),
    onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not upload that image"),
  });
  const remove = useMutation({
    mutationFn: () => apiFetch<void>(getDeleteAvatarUrl(), { method: "DELETE" }),
    onSuccess: () => done("Photo removed"),
    onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not remove the photo"),
  });
  const saveState = useMutationActionState(upload);
  const removeState = useMutationActionState(remove);

  const pick = (next: File | undefined) => {
    setError(null);
    // A fresh pick is a fresh attempt: the pill returns from "failed" to idle.
    upload.reset();
    if (!next) return;
    // Checked here too so an obviously wrong file is refused without a round
    // trip; the server checks the bytes regardless, which is the real gate.
    if (!AVATAR_TYPES.includes(next.type)) {
      setError("Choose a PNG, JPEG or WebP image");
      return;
    }
    if (next.size > MAX_AVATAR_BYTES) {
      setError(
        `That image is ${(next.size / 1024 / 1024).toFixed(1)} MB — the limit is ${MAX_AVATAR_BYTES / 1024 / 1024} MB`,
      );
      return;
    }
    setFile(next);
    void readAsDataUrl(next).then(setPreview, () => setPreview(null));
  };

  const reset = () => {
    setFile(null);
    setPreview(null);
    setDragging(false);
    setError(null);
    upload.reset();
    remove.reset();
  };

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setDragging(false);
    pick(e.dataTransfer.files?.[0]);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        // On the way in as well as out: a save closes the sheet from code,
        // which never passes through here, so the next opening is what
        // clears the last pick and the pill's remembered outcome.
        reset();
        setOpen(o);
      }}
    >
      <DialogTrigger asChild>
        <Button variant="secondary" size="sm" className="shrink-0">
          {photo ? "Change photo" : "Add photo"}
        </Button>
      </DialogTrigger>
      <DialogContent title="Change photo" className="max-w-[340px]">
        <div
          role="button"
          tabIndex={0}
          aria-describedby={hintId}
          onClick={() => picker.current?.click()}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              picker.current?.click();
            }
          }}
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          className={cn(
            "cursor-pointer rounded-lg border-[1.5px] border-dashed p-5 text-center transition-colors",
            dragging ? "border-border-strong bg-raised" : "border-text-disabled hover:border-border-strong",
          )}
        >
          {preview ? (
            <img src={preview} alt="" className="mx-auto mb-2 size-11 rounded-full object-cover" />
          ) : (
            <UserAvatar userId={userId} name={name} email={email} className="mx-auto mb-2 size-11" textClassName="text-[15px]" />
          )}
          <p className="text-[12.5px] font-semibold text-text">{file ? file.name : "Drop an image or browse"}</p>
          <p id={hintId} className="mt-[3px] text-[11px] text-text-faint">
            shown as a circle · max 2 MB · stays on your server
          </p>
        </div>
        <input
          ref={picker}
          type="file"
          accept={AVATAR_TYPES.join(",")}
          className="sr-only"
          aria-label="Choose a profile photo"
          onChange={(e) => {
            pick(e.target.files?.[0]);
            // Cleared so choosing the same file twice still fires a change.
            e.target.value = "";
          }}
        />
        {error && (
          <p role="alert" className="mt-2.5 text-[11.5px] leading-snug text-danger">
            {error}
          </p>
        )}
        <div className="mt-2.5 flex items-center justify-end gap-2">
          {photo && (
            <ActionButton
              variant="ghost"
              size="sm"
              state={removeState}
              busyLabel="Removing…"
              successLabel="Removed"
              onClick={() => {
                setError(null);
                remove.mutate();
              }}
            >
              Remove photo
            </ActionButton>
          )}
          <ActionButton
            variant="primary"
            size="sm"
            state={saveState}
            busyLabel="Uploading…"
            successLabel="Saved"
            disabledReason={file ? undefined : "Choose an image first"}
            onClick={() => file && upload.mutate(file)}
          >
            Save
          </ActionButton>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ─── form ───────────────────────────────────────────────────────────────────

function ProfileForm({ who }: { who: Me }) {
  const qc = useQueryClient();
  const name = who.display_name ?? "";
  const timezone = who.timezone ?? "";
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
        toastSuccess("Profile saved");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save your profile"),
    },
  });
  const saveState = useMutationActionState(save);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({ data: { display_name: nextName, timezone: nextZone } });
  };

  return (
    <form onSubmit={submit}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Display name">
          {(id) => (
            <Input
              id={id}
              className="font-sans"
              maxLength={64}
              placeholder={who.email.split("@")[0] ?? ""}
              value={nextName}
              onChange={(e) => setDraftName(e.target.value)}
            />
          )}
        </Field>

        {/* Canvas 13i's own qualifier. The field stays read-only in the form on
            purpose: moving a sign-in address is a re-auth-gated action, not a
            field save, so it opens its own dialog rather than joining the
            Save button below. */}
        <Field label="Email" qualifier="· re-verified on change">
          {(id) => <ChangeEmailControl id={id} current={who.email} role={who.role} />}
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
          nothing most of the time teaches people to ignore it. It stays for the
          2s the pill spends saying "Saved", so the outcome is read where the
          click was made rather than vanishing under the operator's pointer. */}
      {(dirty || saveState !== "idle") && (
        <div className="mt-3 flex items-center gap-3">
          <ActionButton type="submit" variant="primary" state={saveState} busyLabel="Saving…" successLabel="Saved">
            Save changes
          </ActionButton>
          {dirty && (
            <button
              type="button"
              onClick={() => {
                setDraftName(null);
                setDraftZone(null);
                setError(null);
                save.reset();
              }}
              className="text-[12.5px] text-text-mid hover:text-text"
            >
              Discard
            </button>
          )}
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

// ─── security rows ──────────────────────────────────────────────────────────

const rowAction = "shrink-0 text-[12px] font-medium text-text-dim hover:underline";

function SecurityRows() {
  const totp = useGetTotpStatus();
  const sessions = useListSessions();
  // Undefined until the list is in: the password dialog then asks its
  // question without a number rather than claiming "0 other sessions".
  const others = sessions.data ? sessions.data.filter((s) => !s.current).length : undefined;

  return (
    <div className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
      {/* 13i reads "last changed 3 months ago" here; nothing serves a
          password_changed_at yet, so the row says what the action does. */}
      <Row title="Password" detail="Rotate it, and decide what happens to your other devices">
        <ChangePasswordDialog others={others} />
      </Row>

      <Row
        title="Two-factor"
        chip={
          totp.data && (
            <StatusPill status={totp.data.enabled ? "running" : "stopped"}>{totp.data.enabled ? "on" : "off"}</StatusPill>
          )
        }
        detail={
          <RowDetail query={totp}>
            {(t) =>
              t.enabled
                ? `TOTP · ${t.recovery_codes_left} recovery ${t.recovery_codes_left === 1 ? "code" : "codes"} left`
                : "A code is not required at sign-in."
            }
          </RowDetail>
        }
      >
        <Link to="/settings" hash="two-factor" className={rowAction}>
          Manage
        </Link>
      </Row>

      <Row
        title="Sessions"
        detail={<RowDetail query={sessions}>{(list) => (list.length === 1 ? "1 live sign-in" : `${list.length} live sign-ins`)}</RowDetail>}
      >
        <Link to="/settings" hash="sessions" className={rowAction}>
          Review
        </Link>
      </Row>
    </div>
  );
}

function Row({ title, chip, detail, children }: { title: string; chip?: ReactNode; detail: ReactNode; children: ReactNode }) {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <span className="text-[13px] font-semibold text-text">{title}</span> {chip}
        <div className="mt-0.5 text-[11.5px] leading-[1.5] text-text-faint">{detail}</div>
      </div>
      {children}
    </div>
  );
}

/**
 * A row's sub-line, honest about what it knows (ui-principles §5): the line's
 * height while the answer is on its way, a bar once that has taken long enough
 * to notice, the failure with its retry when the answer is not coming, and the
 * sentence once it has arrived — never a default that reads as fact.
 */
function RowDetail<T>({ query, children }: { query: UseQueryResult<T, unknown>; children: (data: T) => string }) {
  const showSkeleton = useSkeletonDelay(query.isPending);
  if (query.isPending) {
    return showSkeleton ? (
      <Skeleton className="my-[3px] h-[11px] w-36 bg-border-subtle" />
    ) : (
      <span aria-hidden className="invisible">
        checking
      </span>
    );
  }
  if (query.isError) {
    return (
      <span className="text-danger" role="alert">
        Couldn't check —{" "}
        <button type="button" onClick={() => void query.refetch()} className="font-medium underline underline-offset-2">
          {query.isFetching ? "retrying…" : "retry"}
        </button>
      </span>
    );
  }
  return <>{children(query.data)}</>;
}

// ─── password ───────────────────────────────────────────────────────────────

/** A hint, not a gate — the server's only rule is the eight-character floor. */
function strength(pw: string): number {
  if (!pw) return 0;
  let score = pw.length >= 8 ? 1 : 0;
  if (pw.length >= 12) score++;
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw) && /\d/.test(pw)) score++;
  if (/[^\w\s]/.test(pw)) score++;
  return score;
}

function ChangePasswordDialog({ others }: { others: number | undefined }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [revoke, setRevoke] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const change = useChangePassword({
    mutation: {
      onSuccess: (res) => {
        const n = res.revoked;
        toastSuccess(n > 0 ? `Password changed · ${n} other session${n === 1 ? "" : "s"} signed out` : "Password changed");
        void qc.invalidateQueries({ queryKey: getListSessionsQueryKey() });
        setOpen(false);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not change your password"),
    },
  });
  const state = useMutationActionState(change);

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
          change.reset();
        }
      }}
    >
      <DialogTrigger asChild>
        <button type="button" className={rowAction}>
          Change
        </button>
      </DialogTrigger>
      {/* 13ag draws this one at 400px, a touch inside the 420 every other
          ordinary modal takes. */}
      <DialogContent title="Change password" className="max-w-[400px]">
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
                status the server will enforce. The bars are the sighted
                reading; the live region below is the same reading in words. */}
            <div className="mt-1.5 flex gap-1" aria-hidden>
              {[1, 2, 3, 4].map((i) => (
                <span
                  key={i}
                  className={cn("h-1 flex-1 rounded-full", i <= score ? "bg-status-running" : "bg-border-subtle")}
                />
              ))}
            </div>
            <span role="status" className="sr-only">
              {next ? `Password strength: ${score} of 4` : ""}
            </span>
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
                {others === undefined ? "other sessions" : `${others} other session${others === 1 ? "" : "s"}`}
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
              state={state}
              busyLabel="Changing…"
              successLabel="Changed"
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

// ─── notify me about ────────────────────────────────────────────────────────

/**
 * The served `available_kinds` is the only authority on what a kind is; the
 * generated enum is a snapshot of that list. The cast records that the server
 * answers 400 to anything it does not recognise — no client-side list to fall
 * out of date.
 */
const asMutedKinds = (kinds: string[]) => kinds as SetInboxPreferencesRequestMutedKindsItem[];

/** `deploy.failed` → "deploy failed": the canvas's chip label for each kind. */
const chipLabel = (kind: string) => kind.replace(/\./g, " ");

function NotifyChips() {
  const qc = useQueryClient();
  const prefs = useGetInboxPreferences();
  const key = getGetInboxPreferencesQueryKey();
  const showSkeleton = useSkeletonDelay(prefs.isPending);

  const save = useSetInboxPreferences({
    mutation: {
      onSuccess: (res) => qc.setQueryData(key, res),
      onError: (e: unknown, vars) => {
        // The refetch puts the chip back where the server has it.
        void qc.invalidateQueries({ queryKey: key });
        toastFailed("Could not save what to notify you about", e, { retry: () => save.mutate(vars) });
      },
    },
  });

  const toggle = (current: InboxPreferences, kind: string) => {
    const muted = current.muted_kinds.includes(kind)
      ? current.muted_kinds.filter((k) => k !== kind)
      : [...current.muted_kinds, kind];
    // The chip answers the click at once (10b: ≤100 ms) and the server's own
    // answer replaces the guess — or, on failure, the refetch above does.
    qc.setQueryData<InboxPreferences>(key, { ...current, muted_kinds: muted });
    save.mutate({ data: { muted_kinds: asMutedKinds(muted) } });
  };

  return (
    <PageState
      query={prefs}
      loading={
        showSkeleton ? (
          <div aria-busy className="flex flex-wrap gap-[7px]">
            {[96, 96, 112, 120].map((w, i) => (
              <Skeleton key={i} className="h-[27px] rounded-full bg-border-subtle" style={{ width: w }} />
            ))}
          </div>
        ) : null
      }
      isEmpty={(p) => p.available_kinds.length === 0}
      empty={<p className="text-[12.5px] text-text-mid">This panel has nothing to tell you about yet.</p>}
    >
      {(p) => (
        <div role="group" aria-label="Notify me about" aria-busy={save.isPending || undefined} className="flex flex-wrap gap-[7px]">
          {p.available_kinds.map((kind) => (
            <ChoiceChip key={kind} selected={!p.muted_kinds.includes(kind)} onToggle={() => toggle(p, kind)}>
              {chipLabel(kind)}
            </ChoiceChip>
          ))}
          <span role="status" className="sr-only">
            {save.isPending ? "Saving…" : ""}
          </span>
        </div>
      )}
    </PageState>
  );
}

// ─── email ──────────────────────────────────────────────────────────────────

/**
 * The read-only address and its Change control (canvas 17d). Only a panel
 * admin may read the mail settings — GET /panel/mail is 403 for a member — so
 * only an admin asks whether a confirmation can be sent, and when it cannot the
 * control says so and points at the Mail tab (ui-principles §11: no dead ends).
 * A member finds out at submit, and is told who to ask.
 */
function ChangeEmailControl({ id, current, role }: { id: string; current: string; role: MeRole }) {
  const isAdmin = atLeast(role, "admin");
  const mail = useGetPanelMail({ query: { enabled: isAdmin } });
  const unconfigured = isAdmin && mail.data?.configured === false;
  return (
    <>
      <div className="flex items-center gap-2">
        <Input id={id} value={current} readOnly className="flex-1" />
        <ChangeEmailDialog
          current={current}
          isAdmin={isAdmin}
          disabledReason={unconfigured ? "Panel mail isn't set up, so a confirmation can't be sent" : undefined}
        />
      </div>
      {unconfigured && (
        <p className="text-xs leading-relaxed text-text-faint">
          Panel mail isn't set up, so a confirmation can't be sent —{" "}
          <Link to="/settings/mail" className="font-medium text-text underline underline-offset-2">
            configure Mail
          </Link>
          .
        </p>
      )}
    </>
  );
}

/**
 * Moving the sign-in address (docs/features/panel-mail.md §3, threat-model
 * §5.10). Modelled on the change-password dialog, because it is the same kind
 * of act: re-authenticated, consequential, and it ends other sessions.
 *
 * Two things must be true before the address moves, and the dialog says both:
 * the current password proves this is not a borrowed session, and a link sent
 * to the new address proves the mailbox is reachable. Neither alone is enough.
 * The second half — the link, opened while signed in — lands on the page
 * itself as ConfirmEmailCard.
 */
function ChangeEmailDialog({
  current,
  isAdmin,
  disabledReason,
}: {
  current: string;
  isAdmin: boolean;
  disabledReason?: string;
}) {
  const [open, setOpen] = useState(false);
  const [next, setNext] = useState("");
  const [password, setPassword] = useState("");
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<ReactNode>(null);

  const request = useRequestEmailChange({
    mutation: {
      onSuccess: () => setSent(true),
      onError: (e: unknown) => {
        if (e instanceof ApiError && e.status === 502) {
          // The panel could not send: the fix is in the Mail tab, and for a
          // member the fix is someone who can open it.
          setError(
            isAdmin ? (
              <>
                {e.message} —{" "}
                <Link to="/settings/mail" className="font-medium underline underline-offset-2">
                  set up Mail
                </Link>{" "}
                and try again.
              </>
            ) : (
              `${e.message} — ask a panel admin to set up Mail.`
            ),
          );
          return;
        }
        setError(e instanceof ApiError ? e.message : "Could not start the change");
      },
    },
  });
  const state = useMutationActionState(request);

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setNext("");
          setPassword("");
          setSent(false);
          setError(null);
          request.reset();
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="secondary" size="sm" className="shrink-0" disabledReason={disabledReason}>
          Change
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Change email"
        description={
          sent
            ? undefined
            : "A confirmation link goes to the new address; the old one gets a notice. Your other sessions sign out when it completes."
        }
      >
        {sent ? (
          <div className="space-y-4">
            <p className="text-[13px] leading-relaxed text-text-mid">
              A confirmation link is on its way to <b className="text-text">{next}</b>. Open it while signed in here —
              the link alone cannot move the account, and it expires in 30 minutes.
            </p>
            <p className="text-[12px] leading-relaxed text-text-faint">
              {current} has been told a change was requested, in case it was not you.
            </p>
            <div className="flex justify-end">
              <DialogClose asChild>
                <Button variant="primary">Done</Button>
              </DialogClose>
            </div>
          </div>
        ) : (
          <form
            onSubmit={(e: FormEvent) => {
              e.preventDefault();
              setError(null);
              request.mutate({ data: { new_email: next, current_password: password } });
            }}
            className="space-y-3"
          >
            <Field label="New email">
              {(id) => (
                <Input
                  id={id}
                  type="email"
                  required
                  autoFocus
                  autoComplete="email"
                  value={next}
                  onChange={(e) => setNext(e.target.value)}
                />
              )}
            </Field>
            <Field label="Current password">
              {(id) => (
                <Input
                  id={id}
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              )}
            </Field>
            {error && (
              <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
                {error}
              </p>
            )}
            <div className="flex items-center justify-end gap-2.5 pt-1">
              <span className="mr-auto text-[11px] text-text-faint">link valid 30 min</span>
              <DialogClose asChild>
                <Button variant="ghost">Cancel</Button>
              </DialogClose>
              <ActionButton
                type="submit"
                variant="accent"
                state={state}
                busyLabel="Sending…"
                successLabel="Sent"
                disabled={next === "" || password === ""}
              >
                Send confirmation →
              </ActionButton>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/**
 * The mailed link lands here, on the page, with its token in the URL (canvas
 * 17d's right half: CONFIRM — BACK IN THE PANEL, SIGNED IN). A card rather
 * than a modal: the operator arrived from their mailbox and should see the
 * profile they are about to change behind the question, not a scrim.
 *
 * The token carries no address and nothing serves the pending change, so the
 * card cannot yet print "old → new"; it names the old address and says where
 * the link went. Expired and reused links fail with one undifferentiated
 * error, by design.
 */
function ConfirmEmailCard({ token, current }: { token: string; current: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const dismiss = () => void navigate({ to: "/settings/profile", search: {}, replace: true });

  const confirm = useConfirmEmailChange({
    mutation: {
      onSuccess: (res) => {
        const n = res.revoked;
        toastSuccess(n > 0 ? `Address changed · ${n} other session${n === 1 ? "" : "s"} signed out` : "Address changed");
        void qc.invalidateQueries({ queryKey: getGetMeQueryKey() });
        void qc.invalidateQueries({ queryKey: getListSessionsQueryKey() });
        dismiss();
      },
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not finish the change"),
    },
  });
  const state = useMutationActionState(confirm);

  return (
    <section aria-labelledby="confirm-email-title">
      <Eyebrow className="mb-3">Confirm — back in the panel, signed in</Eyebrow>
      <div className="rounded-[10px] border-[1.5px] border-border-strong bg-surface px-5 py-[18px]">
        <h3 id="confirm-email-title" className="text-[15px] font-bold text-text">
          Confirm your new email
        </h3>
        <p className="mt-1.5 text-[12.5px] leading-[1.6] text-text-dim">
          {current && (
            <>
              <span className="mono">{current}</span> → <b className="text-text">the address this link was sent to</b>
              <br />
            </>
          )}
          <span className="text-[12px] text-text-faint">
            The link alone isn't enough — you must be signed in here too. A stolen mailbox can't move this account.
          </span>
        </p>
        {error && (
          <p role="alert" className="mt-3 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
            {error} — ask for a fresh link from the Email field below.
          </p>
        )}
        <div className="mt-3 flex flex-wrap gap-2">
          <ActionButton
            variant="primary"
            state={state}
            busyLabel="Confirming…"
            successLabel="Confirmed"
            onClick={() => {
              setError(null);
              confirm.mutate({ data: { token } });
            }}
          >
            Confirm change
          </ActionButton>
          {/* 17d's danger-outline "This wasn't me". It spends every outstanding
              change without applying one, so a second link requested in the
              same breath cannot outlive the cancel of the first. */}
          <CancelEmailChangeButton onDone={dismiss} />
          <Button variant="ghost" onClick={dismiss}>
            Not now
          </Button>
        </div>
      </div>
    </section>
  );
}

/**
 * The pending move, shown to whoever is signed in — which is the point. A
 * confirmation link goes to the NEW address, so an attacker who has the
 * password but not the mailbox leaves a pending change the real owner can see
 * here and undo, and the old address's warning mail is the other half of the
 * same signal.
 *
 * Nothing is pending is a 404, which is an answer rather than a failure, so the
 * card is simply absent then.
 */
function PendingEmailChangeCard() {
  const pending = useGetPendingEmailChange({ query: { retry: false } });
  if (pending.isPending || pending.isError || !pending.data) return null;
  const p = pending.data;
  return (
    <section className="rounded-[10px] border-[1.5px] border-status-degraded/45 bg-status-degraded/[.05] px-5 py-[18px]">
      <h3 className="text-[15px] font-bold text-text">This account is moving</h3>
      <p className="mt-1.5 text-[12.5px] leading-[1.6] text-text-dim">
        A change to <span className="mono text-text">{p.new_email}</span> was requested{" "}
        {relativeTime(p.requested_at)} and is waiting to be confirmed from that mailbox. The link stops working{" "}
        {relativeTime(p.expires_at)}.
      </p>
      <p className="mt-1.5 text-[12px] leading-[1.5] text-text-faint">
        If that was not you, undo it now — and change your password, because whoever asked knew it.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        <CancelEmailChangeButton />
      </div>
    </section>
  );
}

/**
 * "This wasn't me." Deliberately asks for no password: the person who can undo
 * a move they did not request is the person holding the session, and making
 * them find a password first only keeps a hijacked request alive longer.
 */
function CancelEmailChangeButton({ onDone }: { onDone?: () => void }) {
  const qc = useQueryClient();
  const cancel = useCancelEmailChange({
    mutation: {
      onSuccess: (res) => {
        void qc.invalidateQueries({ queryKey: getGetPendingEmailChangeQueryKey() });
        toastSuccess(
          res.cancelled > 0
            ? { title: "Address change cancelled", detail: "Your sign-in address is unchanged." }
            : { title: "Nothing was pending", detail: "There was no change to undo." },
        );
        onDone?.();
      },
      onError: (e: unknown) => toastFailed("Could not cancel the change", e),
    },
  });
  return (
    <ActionButton
      variant="danger"
      state={cancel.isPending ? "busy" : "idle"}
      busyLabel="Cancelling…"
      successLabel="Cancelled"
      onClick={() => cancel.mutate()}
    >
      This wasn’t me
    </ActionButton>
  );
}
