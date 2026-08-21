// Settings · Account: who you are, two-factor authentication, live sessions,
// and API tokens (shown once at creation). Everything here is credential
// management, so every route it calls is session-only server-side — an API
// token cannot reach this page's actions.
//
// The security surfaces share one shape (canvas 13a/13i): a bordered list whose
// rows carry a bold label, an optional tinted status chip, a faint sub-line,
// and a plain text action on the right. Rows are read far more often than they
// are acted on, so the action stays a word rather than a glyph — "Revoke" and
// "Turn off" mean different things, and two bins do not.
import { createFileRoute } from "@tanstack/react-router";
import { Check, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getGetTotpStatusQueryKey,
  getListSessionsQueryKey,
  getListTokensQueryKey,
  useCreateToken,
  useDeleteToken,
  useDisableTotp,
  useEnrollTotp,
  useGetMe,
  useGetTotpStatus,
  useListSessions,
  useListTokens,
  useRevokeOtherSessions,
  useRevokeSession,
  useVerifyTotp,
} from "@/api/gen/auth/auth";
import { Ability } from "@/api/gen/model";
import { CopyButton, CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { QRCode } from "@/components/qr-code";
import { StatusPill } from "@/components/status-badge";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime, timeUntil } from "@/lib/time";
import { cn } from "@/lib/utils";
import { useQueryClient } from "@tanstack/react-query";

export const Route = createFileRoute("/_app/settings/")({ component: AccountTab });

/** The plain word-action that ends a security row (canvas 13a/13i). */
const rowAction = "shrink-0 text-[12px] font-medium hover:underline disabled:no-underline disabled:opacity-50";

function AccountTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "account" }]);
  const me = useGetMe();
  const tokens = useListTokens();

  return (
    <div className="max-w-xl space-y-8">
      <section className="space-y-2">
        <Eyebrow>Account</Eyebrow>
        <div className="rounded-lg border border-border bg-surface p-4 text-[13px]">
          <p className="text-text">{me.data?.email ?? "…"}</p>
          <p className="mono mt-1 text-xs text-text-faint">panel role: {me.data?.role ?? "…"}</p>
        </div>
      </section>

      <TwoFactorSection />
      <SessionsSection />

      <section className="space-y-2">
        <div className="flex items-center justify-between">
          <Eyebrow>API tokens</Eyebrow>
          <CreateTokenDialog />
        </div>
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          A token lets scripts and CI call the CypherPanel API as you, limited to the abilities you give it. The value
          is shown once at creation.
        </p>
        <PageState
          query={tokens}
          empty={
            <EmptyState
              title="No API tokens"
              hint="Create one to drive deploys from CI or the command line."
              action={<CreateTokenDialog primary />}
            />
          }
        >
          {(list) => (
            <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
              {list.map((t) => (
                <TokenRow
                  key={t.id}
                  id={t.id}
                  name={t.name}
                  abilities={t.abilities}
                  lastUsed={t.last_used_at}
                  expires={t.expires_at}
                  created={t.created_at}
                />
              ))}
            </ul>
          )}
        </PageState>
      </section>
    </div>
  );
}

// ─── two-factor ─────────────────────────────────────────────────────────────

// Panel compromise is fleet control (threat-model), so 2FA gets a first-class
// row rather than hiding behind a toggle. The flow is deliberately linear:
// scan → confirm a code → save recovery codes, with no way to leave enrollment
// half-finished (the server only enables after a verified code).
function TwoFactorSection() {
  const status = useGetTotpStatus();
  const enabled = status.data?.enabled ?? false;
  const left = status.data?.recovery_codes_left ?? 0;
  const spent = enabled && left === 0;

  return (
    <section className="space-y-2">
      <Eyebrow>Security</Eyebrow>
      {/* The lead states the account's own posture, not a generic warning: once
          2FA is on, telling the operator their password alone holds the fleet
          is simply false. */}
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        {enabled
          ? "A code from your authenticator is required at every sign-in."
          : "Your password alone controls every server this panel manages — an authenticator app closes that gap."}
      </p>
      <div className="overflow-hidden rounded-lg border border-border bg-surface">
        <div className="flex items-center gap-3 px-4 py-3">
          <div className="min-w-0 flex-1">
            <span className="text-[13px] font-semibold text-text">Two-factor</span>{" "}
            <StatusPill status={enabled ? "running" : "stopped"}>{enabled ? "on" : "off"}</StatusPill>
            {/* One sentence, one colour: when the last recovery code is gone the
                whole line goes amber rather than splitting mid-clause. */}
            <p className={cn("mt-0.5 text-[11.5px]", spent ? "text-status-degraded-text" : "text-text-faint")}>
              {enabled
                ? `TOTP · ${left} recovery ${left === 1 ? "code" : "codes"} left` +
                  // Enrolling while enabled is refused (409, two-factor-auth.md
                  // §2), so the only real path to a fresh set is off then on.
                  (spent ? " · turn two-factor off and on again to get a fresh set" : "")
                : "A code is not required at sign-in."}
            </p>
          </div>
          {enabled ? <DisableTotpDialog /> : <EnrollTotpDialog />}
        </div>
      </div>
    </section>
  );
}

function EnrollTotpDialog() {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  // Two failures, two places: enrollment never started (the QR is missing and
  // the form cannot be completed at all) is a different problem from a code
  // the server rejected, and painting both in the same slot left the dialog
  // with a grey sentence and a submit disabled for no stated reason.
  const [enrollError, setEnrollError] = useState<string | null>(null);
  const [verifyError, setVerifyError] = useState<string | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);

  const enroll = useEnrollTotp({
    mutation: {
      onError: (e: unknown) => setEnrollError(e instanceof Error ? e.message : "Could not start enrollment"),
    },
  });
  const verify = useVerifyTotp({
    mutation: {
      // Deliberately does NOT refresh the TOTP status here. The refetch would
      // report enabled:true, swapping this dialog for the disable control and
      // unmounting the only copy of the recovery codes the operator will ever
      // see. The status is refreshed on acknowledgement instead.
      onSuccess: (res) => setCodes(res.recovery_codes),
      onError: (e: unknown) => setVerifyError(e instanceof Error ? e.message : "That code was not accepted"),
    },
  });

  // Acknowledging is the single exit from the recovery-code step: it is what
  // dismisses the dialog and what refreshes the status.
  const acknowledge = () => {
    setOpen(false);
    void qc.invalidateQueries({ queryKey: getGetTotpStatusQueryKey() });
  };

  const reset = () => {
    setCode("");
    setEnrollError(null);
    setVerifyError(null);
    setCodes(null);
    enroll.reset();
  };

  const retryEnroll = () => {
    setEnrollError(null);
    enroll.mutate();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Once the codes are on screen, 2FA is already enabled and this is the
        // only time they are shown — so Escape, the close button, and an
        // outside click must not discard them. Only acknowledgement closes.
        //
        // The same applies while verification is still in flight: closing then
        // would let the response enable 2FA and return the one-time codes into
        // a dialog nobody is looking at, and reopening would fail with 409
        // (already enabled) leaving the account with no recovery codes at all.
        if (!next && (codes || verify.isPending)) return;
        setOpen(next);
        if (next) {
          reset();
          enroll.mutate();
        } else {
          reset();
        }
      }}
    >
      <DialogTrigger asChild>
        <button type="button" className={cn(rowAction, "text-text-dim")}>
          Turn on
        </button>
      </DialogTrigger>
      {codes ? (
        <DialogContent
          title="Save your recovery codes"
          description="Each code works once, if you lose your authenticator. This is the only time they are shown."
        >
          {/* The codes are already legible in the grid below; the control here
              copies the set. Rendering them a second time inside a one-line
              field only produced a clipped smear of run-together codes. */}
          <div className="mb-1.5 flex items-center justify-between">
            <span className="text-xs font-semibold text-text">{codes.length} codes</span>
            <CopyButton value={codes.join("\n")} label={`Copy all ${codes.length} codes`} />
          </div>
          <ul className="grid grid-cols-2 gap-1.5 rounded-md border border-border bg-raised p-3">
            {codes.map((c) => (
              <li key={c} className="mono text-[13px] tracking-wide text-text">
                {c}
              </li>
            ))}
          </ul>
          <div className="mt-4 flex justify-end">
            <Button variant="primary" onClick={acknowledge}>
              <Check className="h-3.5 w-3.5" /> I saved them
            </Button>
          </div>
        </DialogContent>
      ) : (
        <DialogContent
          title="Turn on two-factor authentication"
          description="Scan this with an authenticator app, then enter the code it shows."
        >
          <form
            onSubmit={(e: FormEvent) => {
              e.preventDefault();
              setVerifyError(null);
              verify.mutate({ data: { code } });
            }}
            className="space-y-4"
          >
            {enroll.data ? (
              <div className="flex flex-col items-center gap-3">
                <QRCode value={enroll.data.otpauth_uri} label="Two-factor enrollment QR code" />
                <details className="w-full">
                  <summary className="cursor-pointer text-[13px] text-text-mid">Can't scan it?</summary>
                  <p className="mt-2 text-[13px] text-text-mid">Enter this key manually:</p>
                  <div className="mt-1.5">
                    <CopyField value={enroll.data.secret} />
                  </div>
                </details>
              </div>
            ) : enrollError ? (
              <div className="space-y-2">
                <p
                  role="alert"
                  className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger"
                >
                  {enrollError}
                </p>
                <Button variant="ghost" size="sm" onClick={retryEnroll}>
                  Try again
                </Button>
              </div>
            ) : (
              <p className="text-[13px] text-text-mid">Preparing…</p>
            )}

            <Field label="Code from your app" error={verifyError ?? undefined}>
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  placeholder="123456"
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                />
              )}
            </Field>
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button variant="ghost">Cancel</Button>
              </DialogClose>
              <ActionButton
                type="submit"
                variant="primary"
                state={verify.isPending ? "busy" : "idle"}
                busyLabel="Confirming…"
                disabled={code.trim() === "" || !enroll.data}
                disabledReason={enrollError ? "Enrollment could not start — try again above" : undefined}
              >
                Confirm and enable
              </ActionButton>
            </div>
          </form>
        </DialogContent>
      )}
    </Dialog>
  );
}

function DisableTotpDialog() {
  const qc = useQueryClient();
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const disable = useDisableTotp({
    mutation: {
      onSuccess: () => {
        toast.success("Two-factor authentication turned off");
        void qc.invalidateQueries({ queryKey: getGetTotpStatusQueryKey() });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "That code was not accepted"),
    },
  });

  return (
    <Dialog onOpenChange={() => { setCode(""); setError(null); }}>
      <DialogTrigger asChild>
        {/* Danger, like "Revoke": this row's action removes a protection. */}
        <button type="button" className={cn(rowAction, "text-danger")}>
          Turn off
        </button>
      </DialogTrigger>
      <DialogContent
        title="Turn off two-factor authentication"
        description="Confirm with a current code or an unused recovery code. Your account will then be password-only."
      >
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            setError(null);
            disable.mutate({ data: { code } });
          }}
          className="space-y-4"
        >
          <Field label="Code" error={error ?? undefined}>
            {(id) => (
              <Input
                id={id}
                required
                autoFocus
                autoComplete="one-time-code"
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            )}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="danger"
              state={disable.isPending ? "busy" : "idle"}
              busyLabel="Turning off…"
              disabled={code.trim() === ""}
            >
              Turn off
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ─── sessions ───────────────────────────────────────────────────────────────

function SessionsSection() {
  const qc = useQueryClient();
  const sessions = useListSessions();
  const invalidate = () => void qc.invalidateQueries({ queryKey: getListSessionsQueryKey() });

  const revokeOthers = useRevokeOtherSessions({
    mutation: {
      onSuccess: (res) => {
        toast.success(res.revoked === 0 ? "No other sessions to sign out" : `Signed out ${res.revoked} other session(s)`);
        invalidate();
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not sign the others out"),
    },
  });

  const others = (sessions.data ?? []).filter((s) => !s.current).length;

  return (
    <section className="space-y-2">
      <Eyebrow>Sessions</Eyebrow>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        Every live sign-in on this account. Sits right under two-factor — sign the others out if anything looks
        unfamiliar.
      </p>
      <PageState query={sessions} empty={<EmptyState title="No active sessions" hint="Sign in to create one." />}>
        {(list) => (
          <>
            {/* The list scrolls inside itself rather than growing the page.
                Sessions are the one list here with no natural ceiling — a
                laptop, a phone and every browser you ever signed in from all
                sit in it — and a page whose other sections get pushed a
                screenful down by a long one is worse at answering the question
                the section exists for. About five rows deep, so there is always
                something cut off to say "keep scrolling". */}
            <div
              className="max-h-[19rem] overflow-y-auto overscroll-contain rounded-lg border border-border bg-surface"
              // Focusable and named, because a region you can only reach with a
              // pointer is not reachable at all (ui-principles §9).
              tabIndex={0}
              role="group"
              aria-label="Live sessions"
            >
              <ul className="divide-y divide-border-subtle">
                {list.map((s) => (
                  <SessionRow
                    key={s.id}
                    id={s.id}
                    current={s.current}
                    created={s.created_at}
                    expires={s.expires_at}
                    onRevoked={invalidate}
                  />
                ))}
              </ul>
            </div>
            {/* The bulk action sits under the list it acts on, with the count
                of what it will take beside it — a pill labelled "everywhere
                else" is otherwise a number you cannot see before clicking. */}
            <div className="mt-3.5 flex items-center gap-3">
              <Button
                variant="secondary"
                size="md"
                disabled={others === 0 || revokeOthers.isPending}
                onClick={() => revokeOthers.mutate()}
              >
                Sign out everywhere else
              </Button>
              <span className="text-[11.5px] text-text-faint">
                {others === 0 ? "no other sessions" : `revokes ${others} session${others === 1 ? "" : "s"}`}
              </span>
            </div>
          </>
        )}
      </PageState>
    </section>
  );
}

function SessionRow({
  id,
  current,
  created,
  expires,
  onRevoked,
}: {
  id: string;
  current: boolean;
  created: string;
  expires: string;
  onRevoked: () => void;
}) {
  const revoke = useRevokeSession({
    mutation: {
      onSuccess: () => {
        toast.success("Session signed out");
        onRevoked();
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not sign that session out"),
    },
  });
  return (
    // The current row is the one you must not revoke by accident, so it is the
    // one lifted onto the highlight surface — and it carries no action at all.
    <li className={cn("flex items-center gap-3 px-4 py-3", current && "bg-raised")}>
      <span className="min-w-0 flex-1">
        {current ? (
          <>
            <span className="text-[13px] font-semibold text-text">This device</span>{" "}
            <StatusPill status="running">current</StatusPill>
          </>
        ) : (
          // No IP, no user-agent (two-factor-auth.md's sibling spec keeps them
          // out); the truncated session id is the only handle there is, and an
          // operator deciding what to sign out needs to see it.
          <span className="mono text-[12.5px] font-medium text-text" title={id}>
            {id.length > 9 ? `${id.slice(0, 9)}…` : id}
          </span>
        )}
        <span className="mono mt-0.5 block text-[11.5px] text-text-faint">
          signed in {relativeTime(created)} · expires {timeUntil(expires)}
        </span>
      </span>
      {!current && (
        <button
          type="button"
          aria-label={`Revoke session ${id}`}
          disabled={revoke.isPending}
          onClick={() => revoke.mutate({ id })}
          className={cn(rowAction, "text-danger")}
        >
          {revoke.isPending ? "Revoking…" : "Revoke"}
        </button>
      )}
    </li>
  );
}

// ─── API tokens ─────────────────────────────────────────────────────────────

function TokenRow({
  id,
  name,
  abilities,
  lastUsed,
  expires,
  created,
}: {
  id: string;
  name: string;
  abilities: string[];
  lastUsed?: string;
  expires?: string;
  created: string;
}) {
  const qc = useQueryClient();
  const del = useDeleteToken({
    mutation: {
      // Nothing pushes token changes down the live stream, so the revoked row
      // sits there looking usable until the list is asked for again.
      onSuccess: () => {
        toast.success(`Revoked ${name}`);
        void qc.invalidateQueries({ queryKey: getListTokensQueryKey() });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not revoke the token"),
    },
  });
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="flex items-center gap-2">
          <span className="truncate text-[13px] font-semibold text-text">{name}</span>
          {abilities.map((a) => (
            <span key={a} className="mono rounded bg-raised px-1.5 py-0.5 text-[10.5px] uppercase text-text-faint">
              {a}
            </span>
          ))}
        </span>
        <span className="mono mt-0.5 text-[11.5px] text-text-faint">
          created {relativeTime(created)}
          {lastUsed ? ` · last used ${relativeTime(lastUsed)}` : " · never used"}
          {expires ? ` · expires ${timeUntil(expires)}` : " · never expires"}
        </span>
      </span>
      <button
        type="button"
        aria-label={`Revoke ${name}`}
        disabled={del.isPending}
        onClick={() => del.mutate({ id })}
        className={cn(rowAction, "text-danger")}
      >
        {del.isPending ? "Revoking…" : "Revoke"}
      </button>
    </li>
  );
}

const ABILITY_HELP: Record<Ability, string> = {
  [Ability.read]: "View everything you can see",
  [Ability.write]: "Create and change resources",
  [Ability.deploy]: "Trigger deploys and rollbacks",
};

/** Canvas 13ac offers a lifetime, not a checkbox — 90 days is its default. */
const EXPIRY_CHOICES = [
  { days: 7, label: "7 days" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "1 year" },
  { days: 0, label: "Never" },
];

function CreateTokenDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [abilities, setAbilities] = useState<Ability[]>([Ability.read, Ability.deploy]);
  const [expiryDays, setExpiryDays] = useState(90);
  const [minted, setMinted] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateToken({
    mutation: {
      // The dialog stays open on the one-time value; the list behind it has to
      // gain the row all the same, or closing reveals a page missing the token
      // that was just made.
      onSuccess: (res) => {
        setMinted((res as { token?: string }).token ?? null);
        void qc.invalidateQueries({ queryKey: getListTokensQueryKey() });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the token"),
    },
  });

  const toggle = (a: Ability) =>
    setAbilities((cur) => (cur.includes(a) ? cur.filter((x) => x !== a) : [...cur, a]));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name, abilities, expires_in_days: expiryDays } });
  };

  // The plain-language line stays on the page rather than in a tooltip
  // (ui-principles §11) — it just follows the selection instead of repeating
  // itself under every chip.
  const chosen = (Object.keys(ABILITY_HELP) as Ability[]).filter((a) => abilities.includes(a));
  const abilityHelp = chosen.length === 0 ? "Pick at least one." : chosen.map((a) => ABILITY_HELP[a]).join(" · ");

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setMinted(null);
          setName("");
          setAbilities([Ability.read, Ability.deploy]);
          setExpiryDays(90);
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New token
        </Button>
      </DialogTrigger>
      {minted === null ? (
        <DialogContent title="New API token">
          <form onSubmit={submit} className="space-y-4">
            {error && (
              <p
                role="alert"
                className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger"
              >
                {error}
              </p>
            )}
            <Field label="Name" qualifier="· what will use it">
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  className="font-sans"
                  placeholder="github-actions-deploy"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              )}
            </Field>
            {/* Least privilege is the default: a CI credential usually needs to
                read and deploy, not to delete the application it deploys. */}
            <fieldset className="space-y-1.5">
              <legend className="text-[12px] font-semibold text-text">Abilities</legend>
              <div className="flex flex-wrap gap-[7px]">
                {(Object.keys(ABILITY_HELP) as Ability[]).map((ability) => {
                  const on = abilities.includes(ability);
                  return (
                    <button
                      key={ability}
                      type="button"
                      aria-pressed={on}
                      onClick={() => toggle(ability)}
                      className={cn(
                        "rounded-full px-3 py-1 text-[12px] transition-colors",
                        on
                          ? "border-[1.5px] border-border-strong bg-surface font-semibold text-text"
                          : "border border-border-input bg-surface text-text-mid hover:border-border-strong hover:text-text",
                      )}
                    >
                      {ability}
                      {on ? " ✓" : ""}
                    </button>
                  );
                })}
              </div>
              <p className="text-[12px] leading-relaxed text-text-mid">{abilityHelp}</p>
            </fieldset>
            {/* Half-width, as in 13ac — the canvas pairs it with a per-project
                scope the API has no field for, so that cell stays empty. */}
            <div className="grid grid-cols-2 gap-3">
              <Field label="Expires">
                {(id) => (
                  <Select
                    id={id}
                    className="font-sans"
                    value={String(expiryDays)}
                    onChange={(e) => setExpiryDays(Number(e.target.value))}
                  >
                    {EXPIRY_CHOICES.map((c) => (
                      <option key={c.days} value={c.days}>
                        {c.label}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            </div>
            <div className="flex items-center justify-end gap-2.5">
              <span className="mr-auto text-[11.5px] text-text-faint">value shown once, then sealed</span>
              <ActionButton
                type="submit"
                variant="primary"
                state={create.isPending ? "busy" : "idle"}
                busyLabel="Creating…"
                disabled={name.trim() === "" || abilities.length === 0}
              >
                Create token
              </ActionButton>
            </div>
          </form>
        </DialogContent>
      ) : (
        <DialogContent title="Copy your token now" description="This is the only time it will be shown. Store it like a password.">
          <CopyField value={minted} />
          <div className="mt-4 flex justify-end">
            <DialogClose asChild>
              <Button variant="primary">Done</Button>
            </DialogClose>
          </div>
        </DialogContent>
      )}
    </Dialog>
  );
}
