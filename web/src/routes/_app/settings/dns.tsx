// Settings · DNS — the panel's DNS provider, and the zones it can manage
// (docs/features/dns-automation.md §6).
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
import { toast } from "sonner";
import { ApiError } from "@/api/client";
import {
  getGetPanelDNSQueryKey,
  getListDNSZonesQueryKey,
  useDeletePanelDNS,
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
import { ActionButton } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { relativeTime } from "@/lib/time";

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
    <div className="max-w-xl space-y-4">
      <section className="space-y-2">
        <Eyebrow>DNS provider</Eyebrow>
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          Connect Cloudflare and two things become true: a domain only routes if it is inside a zone this token can
          see, and the record that makes it resolve is created, updated and removed for you.
        </p>
        <p className="text-[12.5px] leading-[1.5] text-text-faint">
          Until you connect one, nothing changes — domains route exactly as they do today.
        </p>
      </section>
      <PageState query={dns}>{(settings) => (
          <DNSForm
            configured={settings.configured}
            hint={settings.config_hint}
            accountName={settings.account_name}
          />
        )}</PageState>
      {dns.data?.configured && <ZoneList />}
    </div>
  );
}

/** One Cloudflare account, as returned in the 400 that asks which to use. */
type AccountChoice = { id: string; name: string };

function DNSForm({
  configured,
  hint,
  accountName,
}: {
  configured: boolean;
  hint: string;
  accountName?: string;
}) {
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
        toast.success("Cloudflare connected");
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

  const test = useTestPanelDNS({
    mutation: {
      onSuccess: () => toast.success("Cloudflare answered — the token still works"),
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Cloudflare could not be reached"),
    },
  });

  const disconnect = useDeletePanelDNS({
    mutation: {
      onSuccess: () => {
        invalidate();
        toast.success("Cloudflare disconnected — no records were removed");
      },
      onError: (e: unknown) => setError(e instanceof ApiError ? e.message : "Could not disconnect"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({ data: { api_token: token, ...(accountId ? { account_id: accountId } : {}) } });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      {configured && (
        <div className="rounded-lg border border-border bg-surface px-4 py-3">
          <p className="text-[13px] font-semibold text-text">Connected</p>
          {accountName && <p className="mt-0.5 text-[12px] text-text-mid">{accountName}</p>}
          <p className="mono mt-0.5 truncate text-[12px] text-text-faint">{hint}</p>
          <p className="mt-1.5 text-[11.5px] leading-relaxed text-text-faint">
            Saving again replaces the token, which is never shown back.
          </p>
        </div>
      )}

      <Field label="API token" qualifier="· write-only">
        {(id) => (
          <Input
            id={id}
            required
            type="password"
            autoComplete="off"
            placeholder={configured ? "••••••••" : "Cloudflare API token"}
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
        )}
      </Field>
      {choices.length > 0 && (
        <Field label="Cloudflare account" qualifier="· this token reaches more than one">
          {(id) => (
            <Select id={id} value={accountId} onChange={(e) => setAccountId(e.target.value)}>
              {choices.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}

      <div className="rounded-lg border border-border-subtle bg-bg px-3.5 py-3 text-[11.5px] leading-relaxed text-text-mid">
        <p className="font-semibold text-text">Which token to create</p>
        <p className="mt-1">
          In Cloudflare, go to <span className="mono">Manage Account → API Tokens → Create Token</span> and make an{" "}
          <strong className="font-semibold">account-owned</strong> token. Cloudflare recommends these for durable
          integrations because they belong to the account rather than to you — the panel keeps working after you leave.
          A personal token from <span className="mono">My Profile → API Tokens</span> also works.
        </p>
        <p className="mt-1.5">
          Give it two permissions: <span className="mono">Zone → Zone → Read</span> and{" "}
          <span className="mono">Zone → DNS → Edit</span>. Under <em>Zone Resources</em>, include only the zones
          CypherPanel should manage — this token can repoint any zone it covers, including MX records, so give it no
          more than it needs.
        </p>
        <p className="mt-1.5 text-text-faint">
          You do not need to find your account ID. Paste the token and the panel resolves it; if the token reaches
          several accounts, it will ask which one.
        </p>
      </div>

      {error && (
        <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {error}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <ActionButton type="submit" variant="primary" state={save.isPending ? "busy" : "idle"} busyLabel="Connecting…">
          {configured ? "Replace token" : "Connect"}
        </ActionButton>
        <ActionButton
          variant="secondary"
          state={test.isPending ? "busy" : "idle"}
          busyLabel="Checking…"
          disabledReason={configured ? undefined : "Connect a provider first"}
          onClick={() => {
            setError(null);
            test.mutate();
          }}
        >
          Test connection
        </ActionButton>
        {configured && (
          <ConfirmDestructive
            trigger={<ActionButton variant="ghost">Disconnect</ActionButton>}
            title="Disconnect Cloudflare?"
            lead="This does not remove any DNS record — they stay exactly as they are in Cloudflare. What it does:"
            blastRadius={[
              "Every domain becomes unverified, which stops the panel routing traffic at those hostnames",
              "The zone list is cleared, so nothing verifies until you reconnect",
            ]}
            actionLabel="Disconnect"
            pending={disconnect.isPending}
            onConfirm={() => disconnect.mutate()}
          />
        )}
      </div>
    </form>
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
        toast.success("Zones refreshed");
      },
      onError: (e: unknown) => toast.error(e instanceof ApiError ? e.message : "Could not refresh the zones"),
    },
  });

  return (
    <section className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Zones this panel can manage</Eyebrow>
        <ActionButton
          variant="ghost"
          state={refresh.isPending ? "busy" : "idle"}
          busyLabel="Refreshing…"
          onClick={() => refresh.mutate()}
        >
          Refresh
        </ActionButton>
      </div>
      <PageState query={zones}>
        {(list) =>
          list.length === 0 ? (
            <p className="rounded-lg border border-border bg-surface px-4 py-3 text-[12.5px] text-text-mid">
              This token can see no zones. Check it is scoped to the zones CypherPanel should manage.
            </p>
          ) : (
            <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
              {list.map((z) => (
                <li key={z.id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                  <span className="mono truncate text-[12.5px] text-text">{z.name}</span>
                  <span className="shrink-0 text-[11px] text-text-faint">checked {relativeTime(z.refreshed_at)}</span>
                </li>
              ))}
            </ul>
          )
        }
      </PageState>
      <p className="text-[11.5px] leading-relaxed text-text-faint">
        A domain is verified when it falls inside one of these. Add a zone in Cloudflare, refresh, and it becomes
        usable — no need to re-enter the domain.
      </p>
    </section>
  );
}
