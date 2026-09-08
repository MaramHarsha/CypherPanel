// Settings · Mail — the panel's own outbound transport
// (docs/features/panel-mail.md §6, design canvas 17c).
//
// The same shape every other "connection with credentials" screen uses —
// notifiers (2m), registries (6d/9l) — because they are the same object: a
// host, a credential you write but never read back, and a Test that proves it
// before you rely on it.
//
// The password is write-only. Saved settings come back as a hint naming the host
// and the from address, never the credential, which is what makes it safe for
// this page to exist at all.
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
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/mail")({ component: MailTab });

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

  if (!canManage) {
    return (
      <EmptyState
        glyph="✉"
        title="Mail settings need an admin"
        hint="The panel's outbound email is shared infrastructure, so it is managed by panel admins and owners. Ask one if the panel needs to be able to send."
      />
    );
  }

  return (
    <div className="max-w-xl space-y-3.5">
      <p className="text-[12.5px] leading-[1.55] text-text-mid">
        One SMTP transport for mail the panel sends in its own name — email-change confirmations today, invites and
        digests later. Project notifiers keep their own.
      </p>
      <PageState query={mail}>{(settings) => <MailForm hint={settings.config_hint} configured={settings.configured} />}</PageState>
    </div>
  );
}

function MailForm({ hint, configured }: { hint: string; configured: boolean }) {
  const qc = useQueryClient();
  const saved = configured ? parseHint(hint) : null;
  const [host, setHost] = useState("");
  const [port, setPort] = useState("587");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [from, setFrom] = useState("");
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
    save.mutate({ data: { smtp_host: host, smtp_port: portNumber, username, password, from } });
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
              placeholder={saved?.host ?? "smtp.example.com"}
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
          )}
        </Field>
        {/* No TLS picker: the sender is net/smtp, which issues STARTTLS when the
            server offers it and has no implicit-TLS mode to choose. A control
            that could not change that would be a lie. */}
        <Field label="Port" qualifier="· STARTTLS when offered">
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
            placeholder={saved?.from ?? "panel@example.com"}
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
