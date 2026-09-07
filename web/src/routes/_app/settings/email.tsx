// Settings · Email (managed-email.md, canvas 17e–17f).
//
// The sentence that defines this whole screen, and it is on the screen rather
// than only in the spec: the mail lives at a provider. The panel creates the
// DNS, manages the mailboxes, and your servers never send a byte of mail.
//
// That matters here because "self-hosted panel adds email" reads like Postfix
// to most people, and an operator who thinks they are turning on a mail server
// will make different decisions — about DNS, about reputation, about what they
// are responsible for — than one who knows they are configuring a client.
//
// Phases 3 and 4 (webmail) are not built. The screen says so rather than
// leaving an empty tab where an inbox is expected.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  createMailbox,
  getGetMailDomainRecordsQueryKey,
  getListMailDomainsQueryKey,
  resetMailboxPassword,
  useConnectMailProvider,
  useDeleteMailbox,
  useDisableMailDomain,
  useDisconnectMailProvider,
  useEnableMailDomain,
  useGetMailDomainRecords,
  useGetMailProvider,
  useListMailboxes,
  useListMailDomains,
  useRewriteMailDomainRecords,
} from "@/api/gen/panel/panel";
import type { MailDomain, MailRecord, Mailbox } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyButton, CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/email")({ component: EmailTab });

function EmailTab() {
  useCrumbs([{ label: "settings" }, { label: "email" }]);
  const provider = useGetMailProvider({ query: { retry: false } });

  return (
    <div className="max-w-3xl space-y-5">
      <Eyebrow>Email</Eyebrow>
      {/* The architecture, stated where an operator makes the decision. */}
      <p className="max-w-prose text-[12.5px] leading-[1.5] text-text-mid">
        The mail itself lives at a provider. CypherPanel writes the DNS and manages the mailboxes —{" "}
        <strong className="font-medium text-text">your servers never send a byte of mail</strong>. There is no mail
        server here to run, no queue to drain, and no IP reputation to defend.
      </p>

      <PageState query={provider} isEmpty={() => false} skeletonRows={2}>
        {(p) => (p.connected ? <Connected hint={p.config_hint} /> : <NotConnected />)}
      </PageState>
    </div>
  );
}

function NotConnected() {
  return (
    <div className="rounded-lg border border-border bg-surface">
      <EmptyState
        glyph="✉"
        title="No mail provider connected"
        hint="Connect a hosted provider and the panel writes the MX, SPF, DKIM and DMARC records for your verified domains, then manages the mailboxes from here."
        action={<ConnectDialog primary />}
      />
    </div>
  );
}

function Connected({ hint }: { hint: string }) {
  const qc = useQueryClient();
  const domains = useListMailDomains();
  const disconnect = useDisconnectMailProvider({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries();
        toastSuccess({
          title: "Provider disconnected",
          detail: "Your domains and mailboxes at the provider are untouched.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not disconnect", e, { retry: () => disconnect.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-surface px-4 py-3">
        <span className="mono min-w-0 truncate text-[12.5px] text-text">{hint}</span>
        <span className="flex shrink-0 items-center gap-2">
          <ConnectDialog />
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" className="text-danger">
                Disconnect
              </Button>
            }
            title="Disconnect the mail provider?"
            lead="Disconnecting:"
            blastRadius={[
              "the panel forgets the credential and stops managing mailboxes",
              "your domains, mailboxes and mail at the provider are untouched",
              "the DNS records already written stay written — mail keeps flowing",
            ]}
            actionLabel="Disconnect"
            pendingLabel="Disconnecting…"
            pending={disconnect.isPending}
            onConfirm={() => disconnect.mutate()}
          />
        </span>
      </div>

      <section className="space-y-2.5">
        <div className="flex items-center gap-3">
          <Eyebrow>Domains</Eyebrow>
          <span className="ml-auto">
            <EnableDomainDialog />
          </span>
        </div>
        <PageState
          query={domains}
          skeletonRows={2}
          empty={
            <EmptyState
              glyph={null}
              className="py-6"
              title="No domains have mail enabled"
              hint="Pick a domain the panel already writes DNS for. It gets the provider's MX, SPF, DKIM and DMARC records."
              action={<EnableDomainDialog primary />}
            />
          }
        >
          {(list) => (
            <div className="space-y-3.5">
              {list.map((d: MailDomain) => (
                <DomainCard key={d.id} domain={d} />
              ))}
            </div>
          )}
        </PageState>
      </section>

      {/* Said rather than left as an absent tab, so nobody waits for an inbox
          that is not coming in this release. */}
      <p className="text-[11.5px] leading-[1.5] text-text-faint">
        Reading and writing mail in the panel is not built. Use any mail client with the provider's IMAP and SMTP
        settings — the mailboxes here are ordinary accounts.
      </p>
    </div>
  );
}

function DomainCard({ domain: d }: { domain: MailDomain }) {
  const qc = useQueryClient();
  const [showRecords, setShowRecords] = useState(!d.records_written_at);
  const records = useGetMailDomainRecords(d.id, { query: { enabled: showRecords, retry: false } });
  const mailboxes = useListMailboxes(d.id, { query: { retry: false } });

  const rewrite = useRewriteMailDomainRecords({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListMailDomainsQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetMailDomainRecordsQueryKey(d.id) });
        toastSuccess("Records written");
      },
      onError: (e: unknown, vars) => toastFailed("Could not write the records", e, { retry: () => rewrite.mutate(vars) }),
    },
  });
  const disable = useDisableMailDomain({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListMailDomainsQueryKey() });
        toastSuccess("Stopped managing this domain's mail");
      },
      onError: (e: unknown, vars) => toastFailed("Could not disable", e, { retry: () => disable.mutate(vars) }),
    },
  });

  const live = Boolean(d.records_written_at);

  return (
    <div className="rounded-lg border border-border bg-surface">
      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
        <div className="min-w-0">
          <span className="mono text-[13px] font-medium text-text">{d.domain}</span>
          <p className="mono mt-0.5 text-[11px]">
            {live ? (
              <span className="text-status-running">● LIVE · records written {relativeTime(d.records_written_at ?? "")}</span>
            ) : (
              <span className="text-status-degraded-text">PENDING · the records have not all landed</span>
            )}
          </p>
          {d.last_error && <p className="mt-1 text-[12px] leading-[1.5] text-danger">{d.last_error}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <Button size="sm" variant="ghost" aria-pressed={showRecords} onClick={() => setShowRecords((v) => !v)}>
            Records
          </Button>
          {!live && (
            <ActionButton
              size="sm"
              variant="secondary"
              state={rewrite.isPending ? "busy" : "idle"}
              busyLabel="Writing…"
              onClick={() => rewrite.mutate({ id: d.id })}
            >
              Write them
            </ActionButton>
          )}
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" aria-label={`Stop managing ${d.domain}`}>
                <Trash2 className="h-3.5 w-3.5 text-danger" aria-hidden />
              </Button>
            }
            title={`Stop managing mail for ${d.domain}?`}
            lead="This only stops the panel managing it:"
            blastRadius={[
              "the domain, its mailboxes and its mail stay at the provider",
              "the DNS records already written stay written, so mail keeps flowing",
              "you would manage mailboxes at the provider instead of here",
            ]}
            actionLabel="Stop managing"
            pendingLabel="Removing…"
            pending={disable.isPending}
            onConfirm={() => disable.mutate({ id: d.id })}
          />
        </div>
      </div>

      {showRecords && (
        <div className="border-t border-border px-4 py-3">
          <PageState query={records} isEmpty={() => false} skeletonRows={4}>
            {(r) => (
              <ul className="space-y-2">
                {r.records.map((rec: MailRecord) => (
                  <li key={`${rec.type}-${rec.name}-${rec.content}`} className="space-y-0.5">
                    <div className="flex flex-wrap items-baseline gap-2">
                      <span className="mono text-[11px] font-medium text-text-mid">{rec.type}</span>
                      <span className="mono min-w-0 truncate text-[12px] text-text">{rec.name}</span>
                      {rec.priority ? (
                        <span className="mono text-[11px] text-text-faint">priority {rec.priority}</span>
                      ) : null}
                      <span className="ml-auto shrink-0">
                        <CopyButton value={rec.content} label={`Copy the ${rec.type} value`} />
                      </span>
                    </div>
                    <code className="mono block break-all text-[11.5px] text-text-faint">{rec.content}</code>
                    {/* The purpose, because four TXT records are otherwise
                        indistinguishable and one of them gets deleted. */}
                    <p className="text-[11.5px] leading-[1.5] text-text-mid">{rec.purpose}</p>
                  </li>
                ))}
              </ul>
            )}
          </PageState>
        </div>
      )}

      <div className="border-t border-border px-4 py-3">
        <div className="flex items-center gap-3">
          <p className="text-[10.5px] font-semibold tracking-[0.06em] text-text-faint uppercase">Mailboxes</p>
          <span className="ml-auto">
            <MailboxDialog domainId={d.id} domainName={d.domain} />
          </span>
        </div>
        <PageState
          query={mailboxes}
          skeletonRows={2}
          empty={
            <p className="py-3 text-[12.5px] text-text-mid">
              No mailboxes yet. Creating one shows its password once — the panel never sees it again.
            </p>
          }
        >
          {(boxes) => (
            <ul className="mt-2 divide-y divide-border-subtle">
              {boxes.map((b: Mailbox) => (
                <MailboxRow key={b.address} domainId={d.id} mailbox={b} />
              ))}
            </ul>
          )}
        </PageState>
      </div>
    </div>
  );
}

function MailboxRow({ domainId, mailbox: b }: { domainId: string; mailbox: Mailbox }) {
  const qc = useQueryClient();
  const [issued, setIssued] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const del = useDeleteMailbox({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries();
        toastSuccess("Mailbox deleted");
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the mailbox", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <li className="py-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="mono min-w-0 truncate text-[12.5px] text-text">{b.address}</span>
        <span className="flex shrink-0 items-center gap-1.5">
          <ActionButton
            size="sm"
            variant="ghost"
            state={busy ? "busy" : "idle"}
            busyLabel="…"
            onClick={async () => {
              setBusy(true);
              try {
                const res = await resetMailboxPassword(domainId, { address: b.address });
                setIssued(res.password);
              } catch (e) {
                toastFailed("Could not reset the password", e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Reset password
          </ActionButton>
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" aria-label={`Delete ${b.address}`}>
                <Trash2 className="h-3.5 w-3.5 text-danger" aria-hidden />
              </Button>
            }
            title={`Delete ${b.address}?`}
            lead="Deleting this mailbox:"
            blastRadius={[
              "the account and everything in it is removed at the provider",
              "mail sent to this address afterwards bounces",
              "this cannot be undone from the panel",
            ]}
            confirmName={b.address}
            actionLabel="Delete mailbox"
            pendingLabel="Deleting…"
            pending={del.isPending}
            onConfirm={() => del.mutate({ id: domainId, params: { address: b.address } })}
          />
        </span>
      </div>
      {issued && (
        <div className="mt-2 space-y-1.5 rounded-md border border-pane-border bg-pane px-3 py-2.5">
          <CopyField value={issued} />
          <p className="text-[11.5px] leading-[1.5] text-pane-text">
            Shown once. The panel does not store it and cannot show it again — the provider holds the hash and this
            was forwarded through.
          </p>
        </div>
      )}
    </li>
  );
}

function ConnectDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [account, setAccount] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [error, setError] = useState<string | null>(null);

  const connect = useConnectMailProvider({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries();
        setOpen(false);
        setApiKey("");
        toastSuccess("Mail provider connected");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not connect"),
    },
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          {primary ? "Connect a provider" : "Change"}
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Connect a mail provider"
        description="The credential is checked before it is stored, then sealed under the panel's master key and never shown again."
      >
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            setError(null);
            connect.mutate({ data: { account: account.trim(), api_key: apiKey.trim() } });
          }}
          className="space-y-4"
        >
          <Field label="Admin account" hint="The address your provider API key belongs to.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                required
                autoFocus
                value={account}
                onChange={(e) => setAccount(e.target.value)}
                placeholder="admin@example.com"
                className="mono"
                spellCheck={false}
              />
            )}
          </Field>
          <Field label="API key">
            {(id) => (
              <Input
                id={id}
                required
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                className="mono"
                autoComplete="off"
              />
            )}
          </Field>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={connect.isPending ? "busy" : "idle"}
              busyLabel="Checking…"
            >
              Connect
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EnableDomainDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [domainName, setDomainName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const enable = useEnableMailDomain({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListMailDomainsQueryKey() });
        setOpen(false);
        setDomainName("");
        toastSuccess({ title: "Mail enabled", detail: "The records are written through your DNS provider." });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not enable mail"),
    },
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          <Plus className="h-3.5 w-3.5" aria-hidden /> Enable a domain
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Enable mail on a domain"
        description="It must be a domain this panel already writes DNS for — otherwise the records would have to be added by hand, and the panel would be claiming to have done something it did not."
      >
        <form
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            setError(null);
            enable.mutate({ data: { domain: domainName.trim().toLowerCase() } });
          }}
          className="space-y-4"
        >
          <Field label="Domain">
            {(id) => (
              <Input
                id={id}
                required
                autoFocus
                value={domainName}
                onChange={(e) => setDomainName(e.target.value)}
                placeholder="example.com"
                className="mono"
                spellCheck={false}
              />
            )}
          </Field>
          <p className="text-[12px] leading-[1.5] text-text-faint">
            The provider generates the DKIM key pair and publishes only the public half — the panel never holds a
            signing key. Everything else is MX, SPF and DMARC, and each record says on the screen what it is for.
          </p>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={enable.isPending ? "busy" : "idle"}
              busyLabel="Enabling…"
            >
              Enable mail
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function MailboxDialog({ domainId, domainName }: { domainId: string; domainName: string }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [local, setLocal] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Held only until the dialog closes: the API returns it exactly once.
  const [issued, setIssued] = useState<{ address: string; password: string } | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await createMailbox(domainId, { local_part: local.trim().toLowerCase(), name: name.trim() });
      void qc.invalidateQueries();
      setIssued({ address: res.mailbox.address, password: res.password });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create the mailbox");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setIssued(null);
          setLocal("");
          setName("");
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="secondary" size="sm">
          <Plus className="h-3.5 w-3.5" aria-hidden /> Mailbox
        </Button>
      </DialogTrigger>
      <DialogContent
        title={issued ? "Save this password" : `New mailbox on ${domainName}`}
        description={issued ? undefined : "The password is shown once and the panel never sees it again."}
      >
        {issued ? (
          <div className="space-y-3">
            <p className="mono text-[12.5px] text-text">{issued.address}</p>
            <CopyField value={issued.password} />
            <p className="text-[12px] leading-[1.5] text-text-mid">
              This is the only time it is shown. The panel does not store it and cannot show it again — the provider
              holds the hash. Reset it here if it is lost.
            </p>
            <div className="flex justify-end">
              <DialogClose asChild>
                <Button variant="primary" size="lg">
                  Done
                </Button>
              </DialogClose>
            </div>
          </div>
        ) : (
          <form onSubmit={submit} className="space-y-4">
            <Field
              label="Address"
              hint="Lowercase letters, digits, and . - _ + — narrower than the email standard allows, because the wider forms break in half the clients that receive them."
            >
              {(id, describedBy) => (
                <div className="flex items-center gap-1.5">
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    required
                    autoFocus
                    value={local}
                    onChange={(e) => setLocal(e.target.value)}
                    placeholder="hello"
                    className="mono"
                    spellCheck={false}
                  />
                  <span className={cn("mono shrink-0 text-[12.5px] text-text-faint")}>@{domainName}</span>
                </div>
              )}
            </Field>
            <Field label="Name" qualifier="· optional">
              {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="Support" />}
            </Field>
            {error && (
              <p role="alert" className="text-[13px] text-danger">
                {error}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button type="button" variant="ghost" size="lg">
                  Cancel
                </Button>
              </DialogClose>
              <ActionButton type="submit" variant="primary" size="lg" state={busy ? "busy" : "idle"} busyLabel="Creating…">
                Create mailbox
              </ActionButton>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
