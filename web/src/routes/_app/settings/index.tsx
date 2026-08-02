// Settings · Account: who you are, two-factor authentication, live sessions,
// and API tokens (shown once at creation). Everything here is credential
// management, so every route it calls is session-only server-side — an API
// token cannot reach this page's actions.
import { createFileRoute } from "@tanstack/react-router";
import { Check, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getGetTotpStatusQueryKey,
  getListSessionsQueryKey,
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
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { QRCode } from "@/components/qr-code";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { relativeTime } from "@/lib/time";
import { useQueryClient } from "@tanstack/react-query";

export const Route = createFileRoute("/_app/settings/")({ component: AccountTab });

function AccountTab() {
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
        <p className="text-[13px] text-text-mid">
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
            <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
              {list.map((t) => (
                <TokenRow
                  key={t.id}
                  id={t.id}
                  name={t.name}
                  abilities={t.abilities}
                  lastUsed={t.last_used_at}
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
// section rather than hiding behind a toggle. The flow is deliberately linear:
// scan → confirm a code → save recovery codes, with no way to leave enrollment
// half-finished (the server only enables after a verified code).
function TwoFactorSection() {
  const status = useGetTotpStatus();
  const enabled = status.data?.enabled ?? false;
  const left = status.data?.recovery_codes_left ?? 0;

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between">
        <Eyebrow>Two-factor authentication</Eyebrow>
        {enabled ? <DisableTotpDialog /> : <EnrollTotpDialog />}
      </div>
      <div className="rounded-lg border border-border bg-surface p-4 text-[13px]">
        {enabled ? (
          <>
            <p className="flex items-center gap-2 text-text">
              <ShieldCheck className="h-4 w-4 text-success" aria-hidden /> Enabled — a code is required at every sign-in.
            </p>
            <p className="mono mt-1 text-xs text-text-faint">
              {left} recovery {left === 1 ? "code" : "codes"} remaining
              {left === 0 ? " · re-enroll to get a fresh set" : ""}
            </p>
          </>
        ) : (
          <p className="text-text-mid">
            Off. Your password alone controls every server this panel manages — an authenticator app closes that gap.
          </p>
        )}
      </div>
    </section>
  );
}

function EnrollTotpDialog() {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);

  const enroll = useEnrollTotp({
    mutation: { onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not start enrollment") },
  });
  const verify = useVerifyTotp({
    mutation: {
      // Deliberately does NOT refresh the TOTP status here. The refetch would
      // report enabled:true, swapping this dialog for the disable control and
      // unmounting the only copy of the recovery codes the operator will ever
      // see. The status is refreshed on acknowledgement instead.
      onSuccess: (res) => setCodes(res.recovery_codes),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "That code was not accepted"),
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
    setError(null);
    setCodes(null);
    enroll.reset();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Once the codes are on screen, 2FA is already enabled and this is the
        // only time they are shown — so Escape, the close button, and an
        // outside click must not discard them. Only acknowledgement closes.
        if (!next && codes) return;
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
        <Button variant="primary" size="md">
          <ShieldCheck className="h-3.5 w-3.5" /> Turn on
        </Button>
      </DialogTrigger>
      {codes ? (
        <DialogContent
          title="Save your recovery codes"
          description="Each code works once, if you lose your authenticator. This is the only time they are shown."
        >
          <ul className="grid grid-cols-2 gap-1.5 rounded-md border border-border bg-raised p-3">
            {codes.map((c) => (
              <li key={c} className="mono text-[13px] tracking-wide text-text">
                {c}
              </li>
            ))}
          </ul>
          <div className="mt-3">
            <CopyField value={codes.join("\n")} />
          </div>
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
              setError(null);
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
            ) : (
              <p className="text-[13px] text-text-mid">{enroll.isPending ? "Preparing…" : (error ?? "")}</p>
            )}

            <Field label="Code from your app" error={error ?? undefined}>
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
              <Button type="submit" variant="primary" disabled={verify.isPending || !enroll.data || code.trim() === ""}>
                {verify.isPending ? "Confirming…" : "Confirm and enable"}
              </Button>
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
        <Button variant="ghost" size="md">
          Turn off
        </Button>
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
            <Button type="submit" variant="danger" disabled={disable.isPending || code.trim() === ""}>
              {disable.isPending ? "Turning off…" : "Turn off"}
            </Button>
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
      <div className="flex items-center justify-between">
        <Eyebrow>Sessions</Eyebrow>
        <Button
          variant="ghost"
          size="md"
          disabled={others === 0 || revokeOthers.isPending}
          onClick={() => revokeOthers.mutate()}
        >
          Sign out everywhere else
        </Button>
      </div>
      <p className="text-[13px] text-text-mid">Every device currently signed in to this account.</p>
      <PageState query={sessions} empty={<EmptyState title="No active sessions" hint="Sign in to create one." />}>
        {(list) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
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
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <span className="flex min-w-0 flex-col">
        <span className="truncate text-[13px] text-text">
          {current ? "This device" : "Signed-in device"}
          {current && <span className="ml-2 rounded bg-accent/15 px-1.5 py-0.5 text-[10px] uppercase text-accent">current</span>}
        </span>
        <span className="mono text-xs text-text-faint">
          started {relativeTime(created)} · expires {relativeTime(expires)}
        </span>
      </span>
      <Button
        size="sm"
        variant="ghost"
        aria-label="Sign this session out"
        disabled={revoke.isPending}
        onClick={() => revoke.mutate({ id })}
      >
        <Trash2 className="h-3.5 w-3.5 text-danger" />
      </Button>
    </li>
  );
}

// ─── API tokens ─────────────────────────────────────────────────────────────

function TokenRow({
  id,
  name,
  abilities,
  lastUsed,
  created,
}: {
  id: string;
  name: string;
  abilities: string[];
  lastUsed?: string;
  created: string;
}) {
  const del = useDeleteToken({
    mutation: {
      onSuccess: () => toast.success(`Revoked ${name}`),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not revoke the token"),
    },
  });
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <span className="flex min-w-0 flex-col">
        <span className="flex items-center gap-2">
          <span className="truncate text-[13px] text-text">{name}</span>
          {abilities.map((a) => (
            <span key={a} className="mono rounded bg-raised px-1.5 py-0.5 text-[10px] uppercase text-text-faint">
              {a}
            </span>
          ))}
        </span>
        <span className="mono text-xs text-text-faint">
          created {relativeTime(created)}
          {lastUsed ? ` · last used ${relativeTime(lastUsed)}` : " · never used"}
        </span>
      </span>
      <Button size="sm" variant="ghost" aria-label={`Revoke ${name}`} disabled={del.isPending} onClick={() => del.mutate({ id })}>
        <Trash2 className="h-3.5 w-3.5 text-danger" />
      </Button>
    </li>
  );
}

const ABILITY_HELP: Record<Ability, string> = {
  [Ability.read]: "View everything you can see",
  [Ability.write]: "Create and change resources",
  [Ability.deploy]: "Trigger deploys and rollbacks",
};

function CreateTokenDialog({ primary }: { primary?: boolean }) {
  const [name, setName] = useState("");
  const [abilities, setAbilities] = useState<Ability[]>([Ability.read, Ability.deploy]);
  const [minted, setMinted] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateToken({
    mutation: {
      onSuccess: (res) => setMinted((res as { token?: string }).token ?? null),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the token"),
    },
  });

  const toggle = (a: Ability) =>
    setAbilities((cur) => (cur.includes(a) ? cur.filter((x) => x !== a) : [...cur, a]));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name, abilities } });
  };

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setMinted(null);
          setName("");
          setAbilities(["read", "deploy"]);
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
        <DialogContent title="Create an API token" description="Name it after what will use it — 'ci', 'deploy script'.">
          <form onSubmit={submit} className="space-y-4">
            <Field label="Name" error={error ?? undefined}>
              {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} />}
            </Field>
            {/* Least privilege is the default: a CI credential usually needs to
                read and deploy, not to delete the application it deploys. */}
            <fieldset className="space-y-1.5">
              <legend className="text-[13px] text-text">What it may do</legend>
              {(Object.keys(ABILITY_HELP) as Ability[]).map((ability) => (
                <label key={ability} className="flex items-start gap-2.5 text-[13px]">
                  <input
                    type="checkbox"
                    className="mt-0.5"
                    checked={abilities.includes(ability)}
                    onChange={() => toggle(ability)}
                  />
                  <span>
                    <span className="mono uppercase text-text">{ability}</span>
                    <span className="ml-2 text-text-faint">{ABILITY_HELP[ability]}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button variant="ghost">Cancel</Button>
              </DialogClose>
              <Button type="submit" variant="primary" disabled={create.isPending || name.trim() === "" || abilities.length === 0}>
                {create.isPending ? "Creating…" : "Create token"}
              </Button>
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
