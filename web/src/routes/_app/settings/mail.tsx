// Settings · Mail — the panel's own outbound transport
// (docs/features/panel-mail.md §6, design canvas 17c).
//
// The same shape every other "connection with credentials" screen uses —
// notifiers (2m), registries (6d/9l) — because they are the same object: a
// host, a credential you write but never read back, and a Test that proves it
// before you rely on it.
//
// The password is write-only, and it is the ONLY field that is: the host, the
// port, the username, the from address and the transport mode all read back, so
// changing the port on a working transport is changing the port rather than
// retyping the whole connection and hoping. That is what the API has always
// offered (`PanelMailSettings`); this screen used to ignore it and show the
// saved values as placeholders, which meant every edit was a re-entry and a
// mistyped host silently replaced a good one.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import {
  getGetPanelMailQueryKey,
  useDeletePanelMail,
  useGetPanelMail,
  useSetPanelMail,
  useTestPanelMail,
} from "@/api/gen/panel/panel";
import { useGetMe } from "@/api/gen/auth/auth";
import type { PanelMailSettings, SetPanelMailRequestTls } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Button } from "@/components/ui/button";
import { PageState } from "@/components/page-state";
import { PanelRoleRefusal } from "@/components/role-refusal";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/mail")({ component: MailTab });

/** What each mode actually does, in the words the API's own description uses. */
const TLS_COPY: Record<SetPanelMailRequestTls, string> = {
  starttls: "· upgrades the connection, and refuses to send if the server will not",
  implicit: "· TLS from the first byte",
  none: "· in the clear; only defensible for a relay you control",
};

/** The port each mode conventionally uses. A default, never a lock. */
const TLS_PORT: Record<SetPanelMailRequestTls, string> = { starttls: "587", implicit: "465", none: "25" };

/**
 * The hint is the non-secret half, "smtp.acme.com → ops@acme.com" (core/mail
 * Hint). Split, it names the saved host and the address a test is sent to —
 * the only way this page learns either, since GET never returns the config.
 */
function parseHint(hint: string): { host: string; from: string } | null {
  const [host, from] = hint.split(" → ");
  return host && from ? { host, from } : null;
}

function MailTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "mail" }]);
  const me = useGetMe();
  const canManage = atLeast(me.data?.role as Role | undefined, "admin");
  const mail = useGetPanelMail({ query: { enabled: canManage } });

  // Gated on the answer, not on its absence: a refusal painted while /auth/me
  // is still in flight would flash a 403 at the admin who is allowed here.
  if (me.isSuccess && !canManage) {
    return <PanelRoleRefusal action="Managing the panel's mail transport" needs="admin" />;
  }

  return (
    <div className="max-w-xl space-y-3.5">
      <p className="text-[12.5px] leading-[1.55] text-text-mid">
        One SMTP transport for mail the panel sends in its own name — email-change confirmations today, invites and
        digests later. Project notifiers keep their own.
      </p>
      {/* Keyed on what came back, so a fresh answer after a save or a forget
          re-seeds the fields rather than leaving the old typing in place. */}
      <PageState query={mail}>
        {(settings) => <MailForm key={settings.config_hint} settings={settings} />}
      </PageState>
    </div>
  );
}

function MailForm({ settings }: { settings: PanelMailSettings }) {
  const qc = useQueryClient();
  const { config_hint: hint, configured } = settings;
  const saved = configured ? parseHint(hint) : null;
  // Seeded from what is saved, not from placeholders: an edit is an edit.
  const [host, setHost] = useState(settings.smtp_host ?? "");
  const [port, setPort] = useState(settings.smtp_port ? String(settings.smtp_port) : "587");
  const [username, setUsername] = useState(settings.username ?? "");
  const [password, setPassword] = useState("");
  const [from, setFrom] = useState(settings.from ?? "");
  const [tls, setTls] = useState<SetPanelMailRequestTls>(
    (settings.tls as SetPanelMailRequestTls | undefined) ?? "starttls",
  );
  const [error, setError] = useState<string | null>(null);
  // Who the last test went to. The banner outlives the pill's 2s success hold
  // because "check the inbox" is an instruction, not a flash.
  const [testedTo, setTestedTo] = useState<string | null>(null);

  const save = useSetPanelMail({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPanelMailQueryKey() });
        setPassword("");
        setTestedTo(null);
        toastSuccess("Mail settings saved");
      },
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not save the mail settings"),
    },
  });
  const saveState = useMutationActionState(save);

  // The test reports the server's own words on failure: "connection refused" is
  // the whole answer, and paraphrasing it would only make the operator guess.
  // It is sent to the saved from address (core/mail Test), so that is who the
  // banner names.
  const test = useTestPanelMail({
    mutation: {
      onSuccess: () => {
        setError(null);
        setTestedTo(saved?.from ?? "the from address");
      },
      onError: (e: unknown) => {
        setTestedTo(null);
        setError(e instanceof ApiError ? e.message : "The test message could not be sent");
      },
    },
  });
  const testState = useMutationActionState(test);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const portNumber = Number(port);
    // The API rejects this too; catching it here keeps the operator's typing
    // instead of bouncing them off an alert (ui-principles §1).
    if (!Number.isInteger(portNumber) || portNumber < 1 || portNumber > 65535) {
      setError("The port is a number between 1 and 65535 — 587 is the usual submission port.");
      return;
    }
    setError(null);
    save.mutate({ data: { smtp_host: host, smtp_port: portNumber, username, password, from, tls } });
  };

  return (
    <form onSubmit={submit} className="space-y-3">
      {/* Saved values are never read back (GET returns only the hint), so the
          saved host and from address ride as placeholders — what is there now,
          not what will be sent. Saving replaces the configuration wholesale. */}
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
        <Field label="SMTP host">
          {(id) => (
            <Input
              id={id}
              required
              autoComplete="off"
              spellCheck={false}
              placeholder="smtp.example.com"
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
          )}
        </Field>
        <Field label="Port">
          {(id) => (
            <Input
              id={id}
              required
              inputMode="numeric"
              autoComplete="off"
              value={port}
              onChange={(e) => setPort(e.target.value)}
            />
          )}
        </Field>
      </div>

      {/* The mode is a real choice, not a formality: a provider on 465 speaks
          TLS from the first byte and never offers STARTTLS, so a panel that
          could only do the latter simply could not send through it. Changing
          this moves the port to the one that mode conventionally uses — a port
          and a mode that disagree is the mistake this pairing exists to stop,
          and it is only a default, so a relay on an odd port can still be
          typed. */}
      <Field label="Transport security" qualifier={TLS_COPY[tls]} className="max-w-[320px]">
        {(id) => (
          <Select
            id={id}
            value={tls}
            onChange={(e) => {
              const next = e.target.value as SetPanelMailRequestTls;
              setTls(next);
              setPort(TLS_PORT[next]);
            }}
          >
            <option value="starttls">STARTTLS · 587</option>
            <option value="implicit">Implicit TLS · 465</option>
            <option value="none">None · 25</option>
          </Select>
        )}
      </Field>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Username" qualifier="· empty for an open relay">
          {(id) => (
            <Input
              id={id}
              autoComplete="off"
              spellCheck={false}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          )}
        </Field>
        <Field label="Password" qualifier="· write-only, replaced on save">
          {(id) => (
            <Input
              id={id}
              type="password"
              autoComplete="new-password"
              placeholder={configured ? "•••••••• set" : ""}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
      </div>

      <Field label="From address" qualifier="· what recipients see, and reply to" className="max-w-[320px]">
        {(id) => (
          <Input
            id={id}
            required
            autoComplete="off"
            spellCheck={false}
            placeholder="panel@example.com"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
          />
        )}
      </Field>

      {error && (
        <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {error}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2.5">
        <ActionButton type="submit" variant="primary" state={saveState} busyLabel="Saving…" successLabel="Saved">
          Save
        </ActionButton>
        <ActionButton
          variant="secondary"
          state={testState}
          busyLabel="Sending…"
          successLabel="Sent"
          disabledReason={configured ? undefined : "Save the settings first — a test sends through them"}
          onClick={() => {
            setError(null);
            test.mutate();
          }}
        >
          ↗ Send test email
        </ActionButton>
        {configured && (
          <span className="min-w-0 truncate font-mono text-[11.5px] text-text-faint" title={hint}>
            saved: {hint}
          </span>
        )}
        {/* Forgetting the transport, not editing it. It is a separate act
            because it is the one that stops mail leaving: an invitation still
            gets its link (the create response carries it either way), but an
            email-change confirmation has nowhere to go. */}
        {configured && <ForgetMail onForgotten={() => { setTestedTo(null); setError(null); }} />}
      </div>

      {testedTo && (
        <p
          role="status"
          className="flex items-center gap-2 rounded-md border border-status-running/35 bg-status-running/[0.06] px-[13px] py-[9px] text-[12.5px] text-status-running"
        >
          ✓ Test sent to <span className="font-mono">{testedTo}</span> — check the inbox.
        </p>
      )}
    </form>
  );
}

function ForgetMail({ onForgotten }: { onForgotten: () => void }) {
  const qc = useQueryClient();
  const forget = useDeletePanelMail({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPanelMailQueryKey() });
        onForgotten();
        toastSuccess({
          title: "Mail settings forgotten",
          detail: "The panel can no longer send in its own name.",
        });
      },
      onError: (e: unknown) => toastFailed("Could not forget the mail settings", e),
    },
  });
  return (
    <ConfirmDestructive
      trigger={
        <Button type="button" variant="ghost" size="sm" className="ml-auto text-danger">
          Forget
        </Button>
      }
      title="Forget the mail settings?"
      lead="The panel stops being able to send in its own name:"
      blastRadius={[
        "Email-change confirmations cannot be sent, so nobody can move their sign-in address.",
        "Invitations are still issued — the accept link comes back in the create response either way — but nobody is mailed one.",
        "The SMTP password is destroyed and cannot be recovered; setting mail up again means typing it in.",
        "Project notifiers are untouched — they have their own transports.",
      ]}
      actionLabel="Forget settings"
      pending={forget.isPending}
      pendingLabel="Forgetting…"
      onConfirm={() => forget.mutate()}
    />
  );
}
