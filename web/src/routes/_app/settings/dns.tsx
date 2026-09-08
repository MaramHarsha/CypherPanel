// Settings · DNS — the panel's DNS provider, and the zones it can manage
// (docs/features/dns-automation.md §6, design canvas 17a).
//
// The same shape every other "connection with credentials" screen uses —
// notifiers, registries, mail — because it is the same object: a credential you
// write but never read back, and a Test that proves it before you rely on it.
//
// The one thing this screen adds is the zone list. A token is invisible, so
// "what can this panel actually manage?" has no answer unless the screen shows
// it — and that list is exactly what domain verification is checked against.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import type { DNSSettings } from "@/api/gen/model";
import {
  getGetPanelDNSQueryKey,
  getListDNSZonesQueryKey,
  useDeletePanelDNS,
  useGetDNSDisconnectPreview,
  useGetPanelDNS,
  useListDNSZones,
  useRefreshDNSZones,
  useSetPanelDNS,
  useTestPanelDNS,
} from "@/api/gen/panel/panel";
import { useGetMe } from "@/api/gen/auth/auth";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/dns")({ component: DNSTab });

function DNSTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "dns" }]);
  const me = useGetMe();
  const canManage = atLeast(me.data?.role as Role | undefined, "admin");
  const dns = useGetPanelDNS({ query: { enabled: canManage } });

  if (!canManage) {
    return (
      <EmptyState
        glyph="⌁"
        title="DNS settings need an admin"
        hint="A DNS provider is shared infrastructure — one credential that can write records for every project — so it is managed by panel admins and owners. Ask one if a domain is not verifying."
      />
    );
  }

  return (
    <div className="max-w-xl space-y-3.5">
      <p className="text-[12.5px] leading-[1.55] text-text-mid">
        Connect Cloudflare and domains verify by ownership: a domain is only routed if it falls inside a zone this
        token can see, and the panel creates — and deletes — the A records itself.
      </p>
      <PageState query={dns}>{(settings) => <DNSForm settings={settings} />}</PageState>
      {dns.data?.configured && <ZoneList />}
    </div>
  );
}

/** One Cloudflare account, as returned in the 400 that asks which to use. */
type AccountChoice = { id: string; name: string };

function DNSForm({ settings }: { settings: DNSSettings }) {
  const { configured, account_id: accountIdSaved, account_name: accountName, zone_count: zoneCount } = settings;
  const qc = useQueryClient();
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  // Populated only when Cloudflare tells us the token reaches several accounts.
  // Until then there is no picker, because the common case has nothing to pick.
  const [choices, setChoices] = useState<AccountChoice[]>([]);
  const [accountId, setAccountId] = useState("");

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: getGetPanelDNSQueryKey() });
    void qc.invalidateQueries({ queryKey: getListDNSZonesQueryKey() });
  };

  const save = useSetPanelDNS({
    mutation: {
      onSuccess: () => {
        invalidate();
        setToken("");
        setChoices([]);
        setAccountId("");
        toastSuccess(configured ? "Token replaced — zones re-read" : "Cloudflare connected");
      },
      // Two different failures land here. "This token reaches several accounts"
      // is a question — it arrives with the choices, and turns into a picker
      // rather than an error the operator has to go and solve elsewhere.
      // Everything else is the provider's own words: "Invalid access token" and
      // "missing permission" want different fixes, and paraphrasing would make
      // the operator guess.
      onError: (e: unknown) => {
        const body = e instanceof ApiError ? (e.body as { accounts?: AccountChoice[] } | undefined) : undefined;
        if (body?.accounts?.length) {
          setChoices(body.accounts);
          setAccountId(body.accounts[0]?.id ?? "");
          setError(null);
          return;
        }
        setError(e instanceof ApiError ? e.message : "Could not connect to Cloudflare");
      },
    },
  });
  // The account question is a 400 to the client but not a failure to the
  // operator: the pill goes back to idle and the picker asks it.
  const saveMachine = useMutationActionState(save);
  const saveState = saveMachine === "failed" && choices.length > 0 ? "idle" : saveMachine;

  const test = useTestPanelDNS({
    mutation: {
      onSuccess: () => setError(null),
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Cloudflare could not be reached"),
    },
  });
  const testState = useMutationActionState(test);

  const disconnect = useDeletePanelDNS({
    mutation: {
      onSuccess: () => {
        invalidate();
        toastSuccess("Cloudflare disconnected — no records were removed");
      },
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not disconnect"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({ data: { api_token: token, ...(accountId ? { account_id: accountId } : {}) } });
  };

  // An account-owned token is scoped to one account; a classic user-owned
  // token has none, and that difference is the whole of the guidance below.
  const accountLabel = accountIdSaved ? `${accountName || accountIdSaved} (account-owned)` : "user-owned token";
  const meta = configured ? `${accountName || "user-owned token"} · ${zoneCount} ${zoneCount === 1 ? "zone" : "zones"}` : null;

  return (
    <form onSubmit={submit} className="rounded-lg border border-border bg-surface px-[18px] py-4">
      <div className="mb-3 flex items-center gap-2.5">
        <span className="text-[14px] font-semibold text-text">Cloudflare</span>
        <span
          className={cn(
            "inline-flex items-center gap-1.5 font-mono text-[10.5px] font-medium uppercase tracking-wide",
            configured ? "text-status-running" : "text-text-faint",
          )}
        >
          <StatusDot status={configured ? "running" : "unknown"} className="h-2 w-2" />
          {configured ? "connected" : "not connected"}
        </span>
        {meta && (
          <span className="ml-auto truncate font-mono text-[11.5px] text-text-faint" title={settings.config_hint}>
            {meta}
          </span>
        )}
      </div>

      <div className="mb-3 grid gap-3 sm:grid-cols-2">
        <Field label="API token" qualifier="· write-only">
          {(id) => (
            <Input
              id={id}
              required
              type="password"
              autoComplete="off"
              placeholder={configured ? "•••••••• set" : "Cloudflare API token"}
              value={token}
              onChange={(e) => setToken(e.target.value)}
            />
          )}
        </Field>
        {choices.length > 0 ? (
          <Field label="Account" qualifier="· this token reaches more than one">
            {(id) => (
              <Select id={id} value={accountId} onChange={(e) => setAccountId(e.target.value)}>
                {choices.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name} (account-owned)
                  </option>
                ))}
              </Select>
            )}
          </Field>
        ) : (
          <Field label="Account" qualifier={configured ? undefined : "· resolved from the token"}>
            {(id) => (
              <Input
                id={id}
                readOnly
                value={configured ? accountLabel : ""}
                placeholder="picked when you connect"
                className="text-text-dim"
              />
            )}
          </Field>
        )}
      </div>

      <p className="mb-3 text-[11.5px] leading-[1.5] text-text-faint">
        Use an <b className="font-semibold text-text-dim">account-owned</b> token (Manage Account → API Tokens) with
        Zone:Read + DNS:Edit, scoped to the zones CypherPanel should manage — a personal token dies when its owner
        leaves.
      </p>

      {error && (
        <p role="alert" className="mb-3 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {error}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <ActionButton
          type="submit"
          variant="primary"
          state={saveState}
          busyLabel="Connecting…"
          successLabel={configured ? "Token replaced" : "Connected"}
          // Scoped to idle: the grey reasoned fill would otherwise paint over
          // the green success pill the moment the token field is cleared.
          disabledReason={
            saveState === "idle" && configured && !token ? "Paste a new token to replace the current one" : undefined
          }
        >
          {configured ? "Replace token" : "Connect"}
        </ActionButton>
        <ActionButton
          variant="secondary"
          state={testState}
          busyLabel="Checking…"
          successLabel="Cloudflare answered"
          disabledReason={configured ? undefined : "Connect a provider first"}
          onClick={() => {
            setError(null);
            test.mutate();
          }}
        >
          ↗ Test connection
        </ActionButton>
        {configured && (
          <DisconnectConfirm
            accountName={accountName}
            pending={disconnect.isPending}
            onConfirm={() => disconnect.mutate()}
          />
        )}
      </div>
    </form>
  );
}

/**
 * The disconnect, with its real blast radius rather than a generic one. Only
 * the panel knows which application domains are verified through the connected
 * provider, and "3 domains stop being routed, here they are" is a different
 * decision from "every domain becomes unverified" — so the preview is fetched
 * when the dialog opens (it reads desired state and changes nothing) and its
 * count and names go into the list.
 *
 * When the preview has not arrived, or fails, the generic sentence stands: a
 * confirm that waits for a number before it will let you read it is worse than
 * one that is merely vaguer.
 */
function DisconnectConfirm({
  accountName,
  pending,
  onConfirm,
}: {
  accountName: string | undefined;
  pending: boolean;
  onConfirm: () => void;
}) {
  const preview = useGetDNSDisconnectPreview();
  const n = preview.data?.verified_domain_count;
  const domains = preview.data?.domains ?? [];
  // Enough to recognise what is affected; the full list belongs to the pages
  // that own those applications, not to a confirm dialog.
  const named = domains.slice(0, 4).map((d) => `${d.domain} (${d.application_name})`);
  const rest = domains.length - named.length;

  return (
    <ConfirmDestructive
      trigger={<ActionButton variant="danger">Disconnect…</ActionButton>}
      title="Disconnect Cloudflare?"
      lead="Nothing is deleted at Cloudflare — the records stay exactly as they are. What changes:"
      blastRadius={[
        n === undefined
          ? "Every verified domain becomes unverified, which stops the panel routing traffic at those hostnames"
          : n === 0
            ? "No domain is verified through this provider yet, so no application stops being routed"
            : `${n} verified domain${n === 1 ? "" : "s"} stop being routed: ${named.join(", ")}${rest > 0 ? `, and ${rest} more` : ""}`,
        "The zone list is cleared, so nothing verifies until you reconnect",
        "Records the panel created stay at Cloudflare, now unmanaged — nothing updates or removes them",
      ]}
      confirmName={accountName || "cloudflare"}
      actionLabel="Disconnect"
      pendingLabel="Disconnecting…"
      pending={pending}
      onConfirm={onConfirm}
    />
  );
}

function ZoneList() {
  const qc = useQueryClient();
  const zones = useListDNSZones();
  const refresh = useRefreshDNSZones({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListDNSZonesQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetPanelDNSQueryKey() });
      },
      onError: (e: unknown, vars) =>
        toastFailed("Could not refresh the zones", e, { retry: () => refresh.mutate(vars), id: "dns-zones-refresh" }),
    },
  });
  const refreshState = useMutationActionState(refresh);

  return (
    <section className="space-y-2.5">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Zones — what this token can manage</Eyebrow>
        <ActionButton
          size="sm"
          variant="secondary"
          state={refreshState}
          busyLabel="Refreshing…"
          successLabel="Refreshed"
          onClick={() => refresh.mutate()}
        >
          ↻ Refresh zones
        </ActionButton>
      </div>
      <PageState query={zones}>
        {(list) =>
          list.length === 0 ? (
            <p className="rounded-lg border border-border bg-surface px-4 py-3 text-[12.5px] text-text-mid">
              This token can see no zones. Scope it to the zones CypherPanel should manage, then refresh.
            </p>
          ) : (
            <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
              {list.map((z) => {
                const active = z.status === "active";
                return (
                  <li key={z.id} className="flex items-center gap-3 px-4 py-[11px]">
                    <StatusDot status={active ? "running" : "degraded"} className="h-2 w-2" />
                    <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] font-medium text-text">{z.name}</span>
                    {/* A zone that is not active is still yours and still
                        verifies a domain; the nameserver step is the only
                        thing between it and resolving. Said on the row,
                        where the amber dot asks the question. */}
                    <span
                      className={cn(
                        "shrink-0 font-mono text-[11px]",
                        active ? "text-text-faint" : "text-status-degraded-text",
                      )}
                      title={absoluteTime(z.refreshed_at)}
                    >
                      {active
                        ? `active · checked ${relativeTime(z.refreshed_at)}`
                        : `${z.status} — point nameservers at Cloudflare · still verifies domains`}
                    </span>
                  </li>
                );
              })}
            </ul>
          )
        }
      </PageState>
      <p className="text-[12px] leading-[1.5] text-text-faint">
        Records are grey-cloud (DNS only), so Let's Encrypt keeps working unchanged. The panel only ever touches
        records it created; disconnecting deletes nothing at Cloudflare but unverifies every domain.
      </p>
    </section>
  );
}
