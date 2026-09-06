// Settings · TLS — the panel's ACME account for routed applications
// (docs/features/agent-identity-and-tls.md).
//
// SOURCING NOTE: this screen has no card in the design canvas — TLS became one
// panel-wide account rather than a per-host env var after the canvas was drawn.
// It is built on the Mail tab's shape (canvas 17c), because it is the same
// object: one piece of shared infrastructure, owner-gated, that everything
// downstream silently depends on.
//
// Two things make it unlike every other credential screen, and both are the
// reason it can be a plain form:
//
//   · Nothing here is a secret. The CA publishes the account email back in the
//     registration, and the ACME account KEY — the part that is secret — is
//     generated and kept by the proxy on the serving node, never by the control
//     plane. So the values are shown as stored rather than hinted at.
//   · Unconfigured is a NORMAL state, not an error. Routed applications are
//     served over plain HTTP and say so through `Application.tls_state`; the
//     page says the same thing rather than presenting an empty form as a fault.
//
// Saving is wholesale: the email and the directory are one account, and
// half-changing them would point an existing account at a different CA. An
// empty email clears it — "no email" and "no ACME" are the same statement — so
// there is no separate delete, and the button says which of the two it is
// about to do.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import { getGetPanelTLSQueryKey, useGetPanelTLS, useSetPanelTLS } from "@/api/gen/panel/panel";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { PanelRoleRefusal } from "@/components/role-refusal";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/tls")({ component: TLSTab });

/** The Let's Encrypt staging directory, offered by name rather than left for
 *  the operator to look up — testing against production burns a rate limit
 *  that takes a week to come back. */
const STAGING = "https://acme-staging-v02.api.letsencrypt.org/directory";

function TLSTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "tls" }]);
  const me = useGetMe();
  // Owner only: this one setting decides whether every managed Proxy in the
  // fleet can obtain a certificate at all.
  const canManage = me.data?.role === "owner";
  const tls = useGetPanelTLS({ query: { enabled: canManage } });

  if (me.isSuccess && !canManage) {
    return <PanelRoleRefusal action="Managing the panel's ACME account" needs="owner" />;
  }

  return (
    <div className="max-w-xl space-y-3.5">
      <Eyebrow>TLS</Eyebrow>
      <p className="text-[12.5px] leading-[1.55] text-text-mid">
        One ACME account, carried to every enrolled server inside its desired set. Without it the managed Proxy emits no
        certificate resolver, and routed applications are served over plain HTTP — which they say for themselves, rather
        than pretending to be secure.
      </p>
      <PageState query={tls} isEmpty={() => false} skeletonRows={3}>
        {(settings) => <TLSForm settings={settings} />}
      </PageState>
    </div>
  );
}

function TLSForm({
  settings,
}: {
  settings: { configured: boolean; acme_email: string; acme_ca_server: string; updated_at?: string };
}) {
  const qc = useQueryClient();
  const [email, setEmail] = useState(settings.acme_email);
  const [ca, setCa] = useState(settings.acme_ca_server);
  const [error, setError] = useState<string | null>(null);

  const save = useSetPanelTLS({
    mutation: {
      onSuccess: (next) => {
        void qc.invalidateQueries({ queryKey: getGetPanelTLSQueryKey() });
        setError(null);
        toastSuccess(
          next.configured
            ? {
                title: "ACME account saved",
                detail: "Every enrolled server is asked to re-read its desired set now, so nodes pick it up within a reconcile.",
              }
            : {
                title: "ACME account cleared",
                detail: "New certificates are no longer requested. Certificates already issued keep serving until they expire.",
              },
        );
      },
      onError: (e: unknown) => {
        setError(e instanceof Error ? e.message : "Could not save the ACME account");
        toastFailed("Could not save the ACME account", e);
      },
    },
  });
  const state = useMutationActionState(save);

  const clearing = settings.configured && email.trim() === "";
  const dirty = email.trim() !== settings.acme_email || ca.trim() !== settings.acme_ca_server;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({ data: { acme_email: email.trim(), acme_ca_server: ca.trim() || undefined } });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      {/* The one fact that decides what every https route is actually doing. */}
      <div
        className={
          settings.configured
            ? "rounded-lg border border-status-running/40 bg-status-running/[.06] px-3.5 py-2.5"
            : "rounded-lg border border-border bg-raised px-3.5 py-2.5"
        }
      >
        <p className="text-[12.5px] leading-[1.5] text-text">
          {settings.configured ? (
            <>
              Certificates are being issued for routed applications.
              {settings.updated_at ? <span className="text-text-mid"> Set {relativeTime(settings.updated_at)}.</span> : null}
            </>
          ) : (
            <>
              No ACME account. Routed applications are served over plain HTTP — nothing is broken, and nothing is
              encrypted.
            </>
          )}
        </p>
      </div>

      <Field
        label="Account email"
        qualifier="· registered with the CA"
        hint="A plain address (ops@example.com), not a display-name form. Empty clears the account and turns certificate issuance off fleet-wide."
        error={error ?? undefined}
      >
        {(id, describedBy) => (
          <Input
            id={id}
            type="email"
            aria-describedby={describedBy}
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="ops@example.com"
          />
        )}
      </Field>

      <Field
        label="Directory URL"
        qualifier="· optional"
        hint="Empty means Let's Encrypt production. Point it at staging while testing, so a misconfigured domain does not burn the production rate limit."
      >
        {(id, describedBy) => (
          <Input
            id={id}
            aria-describedby={describedBy}
            value={ca}
            onChange={(e) => setCa(e.target.value)}
            placeholder="https://acme-v02.api.letsencrypt.org/directory"
          />
        )}
      </Field>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <button
          type="button"
          onClick={() => setCa(ca === STAGING ? "" : STAGING)}
          className="mono text-[11.5px] text-text-mid hover:underline"
        >
          {ca === STAGING ? "use production instead" : "use the Let’s Encrypt staging directory"}
        </button>
        <ActionButton
          type="submit"
          variant={clearing ? "danger" : "primary"}
          size="lg"
          state={state}
          busyLabel="Saving…"
          successLabel="Saved"
          disabledReason={!dirty ? "Nothing has changed" : undefined}
        >
          {clearing ? "Clear the account" : "Save account"}
        </ActionButton>
      </div>

      <p className="text-[12px] leading-[1.5] text-text-faint">
        The email and the directory are one account and are replaced together — half-changing them would point an
        existing account at a different CA.
      </p>
    </form>
  );
}
