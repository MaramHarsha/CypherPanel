// Settings · Mail — the panel's own outbound transport
// (docs/features/panel-mail.md §6).
//
// There is no canvas card for this screen; it is inferred from the shape every
// other "connection with credentials" screen uses — notifiers (2m), registries
// (6d/9l) — because they are the same object: a host, a credential you write but
// never read back, and a Test that proves it before you rely on it.
//
// The password is write-only. Saved settings come back as a hint naming the host
// and the from address, never the credential, which is what makes it safe for
// this page to exist at all.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { ApiError } from "@/api/client";
import { getGetPanelMailQueryKey, useGetPanelMail, useSetPanelMail, useTestPanelMail } from "@/api/gen/panel/panel";
import { useGetMe } from "@/api/gen/auth/auth";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";

export const Route = createFileRoute("/_app/settings/mail")({ component: MailTab });

/** The ports people actually use, named — 587 is the one to reach for. */
const PORTS = [
  { value: 587, label: "587 · STARTTLS (usual)" },
  { value: 465, label: "465 · implicit TLS" },
  { value: 25, label: "25 · plain (unauthenticated relays)" },
  { value: 2525, label: "2525 · alternate submission" },
];

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
    <div className="max-w-xl space-y-4">
      <section className="space-y-2">
        <Eyebrow>Panel mail</Eyebrow>
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          How the panel sends its own email — confirming an address change, warning the address being moved away from.
          Separate from a project's notifiers, which tell people about that project's events.
        </p>
      </section>
      <PageState query={mail}>{(settings) => <MailForm hint={settings.config_hint} configured={settings.configured} />}</PageState>
    </div>
  );
}

function MailForm({ hint, configured }: { hint: string; configured: boolean }) {
  const qc = useQueryClient();
  const [host, setHost] = useState("");
  const [port, setPort] = useState(587);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [from, setFrom] = useState("");
  const [error, setError] = useState<string | null>(null);

  const save = useSetPanelMail({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPanelMailQueryKey() });
        setPassword("");
        toast.success("Mail settings saved");
      },
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not save the mail settings"),
    },
  });

  // The test reports the server's own words on failure: "connection refused" is
  // the whole answer, and paraphrasing it would only make the operator guess.
  const test = useTestPanelMail({
    mutation: {
      onSuccess: () => toast.success("Test message sent — check the from address"),
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "The test message could not be sent"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({ data: { smtp_host: host, smtp_port: port, username, password, from } });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      {configured && (
        <div className="rounded-lg border border-border bg-surface px-4 py-3">
          <p className="text-[13px] font-semibold text-text">Configured</p>
          <p className="mono mt-0.5 truncate text-[12px] text-text-faint">{hint}</p>
          <p className="mt-1.5 text-[11.5px] leading-relaxed text-text-faint">
            Saving again replaces all of it — including the password, which is never shown back.
          </p>
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="SMTP host" qualifier="· where mail is handed over">
          {(id) => (
            <Input id={id} required placeholder="smtp.example.com" value={host} onChange={(e) => setHost(e.target.value)} />
          )}
        </Field>
        <Field label="Port">
          {(id) => (
            <Select id={id} className="font-sans" value={port} onChange={(e) => setPort(Number(e.target.value))}>
              {PORTS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Username" qualifier="· leave empty for an open relay">
          {(id) => <Input id={id} value={username} onChange={(e) => setUsername(e.target.value)} />}
        </Field>
        <Field label="Password" qualifier="· write-only">
          {(id) => (
            <Input
              id={id}
              type="password"
              autoComplete="new-password"
              placeholder={configured ? "••••••••" : ""}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
      </div>

      <Field label="From address" qualifier="· what recipients see, and reply to">
        {(id) => (
          <Input id={id} required placeholder="panel@example.com" value={from} onChange={(e) => setFrom(e.target.value)} />
        )}
      </Field>

      {error && (
        <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {error}
        </p>
      )}

      <div className="flex items-center gap-3">
        <ActionButton type="submit" variant="primary" state={save.isPending ? "busy" : "idle"} busyLabel="Saving…">
          {configured ? "Replace settings" : "Save settings"}
        </ActionButton>
        <ActionButton
          variant="secondary"
          state={test.isPending ? "busy" : "idle"}
          busyLabel="Sending…"
          disabledReason={configured ? undefined : "Save the settings first — a test sends through them"}
          onClick={() => {
            setError(null);
            test.mutate();
          }}
        >
          Send test email
        </ActionButton>
      </div>
    </form>
  );
}
