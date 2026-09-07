// Application · Access (canvas 13n). Who reaches the app through the Proxy.
//
// The design card carries three toggles and calls all three "proxy
// middlewares". Half of that is true and the important half is not: Traefik v3
// has `ipAllowList` and `basicAuth`, but no middleware that returns a fixed
// response, so maintenance mode needs a responder service rather than a
// middleware (app-access-control.md §7). The two that ARE middlewares ship
// here; maintenance mode is a separate slice and this card says so rather than
// drawing a dead toggle.
//
// Both of these are CURRENT application state, not part of a revision's
// snapshot, and the card says that too — an operator has to know that rolling
// back will not quietly lift a lockout, because that is exactly the assumption
// that gets someone hurt.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
  getGetApplicationAccessQueryKey,
  useGetApplicationAccess,
  useSetApplicationAccess,
  useSetPreviewPassword,
} from "@/api/gen/applications/applications";
import { CopyButton } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { relativeTime } from "@/lib/time";
import { toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export function AppAccessCard({ appId }: { appId: string }) {
  const access = useGetApplicationAccess(appId);
  return (
    <section className="space-y-2.5">
      <Eyebrow>Access</Eyebrow>
      <PageState query={access} isEmpty={() => false} skeletonRows={3}>
        {(policy) => <AccessBody appId={appId} policy={policy} />}
      </PageState>
    </section>
  );
}

function AccessBody({
  appId,
  policy,
}: {
  appId: string;
  policy: {
    ip_allowlist_enabled: boolean;
    ip_allowlist: string[];
    preview_password_enabled: boolean;
    preview_password_set_at?: string | null;
  };
}) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: getGetApplicationAccessQueryKey(appId) });

  return (
    <div className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
      <Allowlist appId={appId} enabled={policy.ip_allowlist_enabled} cidrs={policy.ip_allowlist} onSaved={refresh} />
      <PreviewPassword
        appId={appId}
        enabled={policy.preview_password_enabled}
        setAt={policy.preview_password_set_at ?? null}
        onSaved={refresh}
      />
      {/* Named rather than omitted: the design card has three toggles, and a
          reader who knows the third exists should learn why it is not here
          instead of assuming it was forgotten. */}
      <div className="px-4 py-3">
        <p className="text-[13px] font-semibold text-text-mid">Maintenance mode</p>
        <p className="mt-0.5 text-[12px] leading-[1.5] text-text-faint">
          Not built yet. Serving a branded 503 needs a responder service on the node — Traefik has no middleware that
          returns a body of ours — so it ships with that responder rather than as a toggle that does nothing.
        </p>
      </div>
    </div>
  );
}

function Allowlist({
  appId,
  enabled,
  cidrs,
  onSaved,
}: {
  appId: string;
  enabled: boolean;
  cidrs: string[];
  onSaved: () => void;
}) {
  const [on, setOn] = useState(enabled);
  const [list, setList] = useState(cidrs.join("\n"));
  const [error, setError] = useState<string | null>(null);

  const save = useSetApplicationAccess({
    mutation: {
      onSuccess: () => {
        setError(null);
        onSaved();
        toastSuccess({
          title: on ? "Allowlist applied" : "Allowlist off",
          detail: "The Proxy picks it up within a reconcile — no deploy needed.",
        });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the allowlist"),
    },
  });

  const entries = list
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
  const dirty = on !== enabled || entries.join("\n") !== cidrs.join("\n");

  return (
    <div className="space-y-2.5 px-4 py-3.5">
      <label className="flex items-start gap-2.5">
        <input
          type="checkbox"
          checked={on}
          onChange={(e) => setOn(e.currentTarget.checked)}
          className="mt-0.5 size-3.5 accent-accent"
        />
        <span className="min-w-0">
          <span className="block text-[13px] font-semibold text-text">IP allowlist</span>
          <span className="block text-[12.5px] leading-[1.5] text-text-mid">
            Only these CIDRs reach the app — for admin panels and internal tools.
          </span>
        </span>
      </label>

      {on && (
        <div className="space-y-2 pl-6">
          <Field
            label="Allowed networks"
            qualifier="· one per line"
            hint="A bare address is read as a single host. Prefixes are masked, so 203.0.113.5/24 is stored as the network it describes."
            error={error ?? undefined}
          >
            {(id, describedBy) => (
              <textarea
                id={id}
                aria-describedby={describedBy}
                value={list}
                onChange={(e) => setList(e.target.value)}
                spellCheck={false}
                rows={Math.max(3, entries.length + 1)}
                className={cn(
                  "w-full rounded-md border border-border-input bg-surface px-3 py-2 font-mono text-[12.5px] text-text",
                  "transition-colors focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none",
                )}
                placeholder={"203.0.113.0/24\n198.51.100.7"}
              />
            )}
          </Field>
          {/* The lockout this feature can cause, said before it can happen. */}
          <p className="text-[12px] leading-[1.5] text-status-degraded-text">
            Everyone not on this list gets a 403 — including you, if your address is not here.
          </p>
        </div>
      )}

      <div className="flex items-center justify-end gap-2.5 pl-6">
        <ActionButton
          variant="secondary"
          size="sm"
          state={save.isPending ? "busy" : "idle"}
          busyLabel="Saving…"
          disabledReason={!dirty ? "Nothing has changed" : on && entries.length === 0 ? "Add at least one CIDR" : undefined}
          onClick={() => {
            setError(null);
            save.mutate({ id: appId, data: { ip_allowlist_enabled: on, ip_allowlist: entries } });
          }}
        >
          Save allowlist
        </ActionButton>
      </div>
    </div>
  );
}

function PreviewPassword({
  appId,
  enabled,
  setAt,
  onSaved,
}: {
  appId: string;
  enabled: boolean;
  setAt: string | null;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [passphrase, setPassphrase] = useState("");
  const [error, setError] = useState<string | null>(null);
  // Held only until the dialog closes: the API returns it exactly once, and
  // keeping it anywhere else would undo the point of not storing it.
  const [issued, setIssued] = useState<string | null>(null);

  const set = useSetPreviewPassword({
    mutation: {
      onSuccess: (res) => {
        setError(null);
        onSaved();
        if (res.passphrase) {
          setIssued(res.passphrase);
          return;
        }
        setOpen(false);
        toastSuccess("Preview passphrase cleared");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not set the passphrase"),
    },
  });

  return (
    <div className="px-4 py-3.5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-semibold text-text">Password-protect previews</p>
          <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
            Every <code className="mono text-[11.5px]">pr-*</code> environment asks for this passphrase before serving —
            clients see staging, the internet doesn’t.
          </p>
          <p className="mono mt-1 text-[11px] text-text-faint">
            {enabled && setAt
              ? `set ${relativeTime(setAt)} · standing environments are unaffected`
              : "off · standing environments are never gated by this"}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => {
              setPassphrase("");
              setIssued(null);
              setError(null);
              setOpen(true);
            }}
          >
            {enabled ? "Rotate" : "Set a passphrase"}
          </Button>
          {enabled && (
            <ActionButton
              variant="ghost"
              size="sm"
              state={set.isPending ? "busy" : "idle"}
              busyLabel="Clearing…"
              onClick={() => set.mutate({ id: appId, data: { passphrase: "" } })}
            >
              Turn off
            </ActionButton>
          )}
        </div>
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent
          title={issued ? "Passphrase set" : enabled ? "Rotate the passphrase" : "Set a preview passphrase"}
          description={
            issued
              ? undefined
              : "Preview environments will ask for it before serving. It is hashed on the way in and shown back exactly once — the panel cannot read it again."
          }
        >
          {issued ? (
            <div className="space-y-3">
              <div className="flex items-start gap-2.5 rounded-md border border-pane-border bg-pane px-3.5 py-3">
                <code className="min-w-0 flex-1 break-all font-mono text-[13px] text-pane-text">{issued}</code>
                <span className="-mr-1 -mt-1 shrink-0">
                  <CopyButton value={issued} label="Copy the passphrase" />
                </span>
              </div>
              <p className="text-[12px] leading-[1.5] text-text-faint">
                This is the only time it is shown. Rotating replaces it; there is no way to read it back.
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
            <form
              onSubmit={(e) => {
                e.preventDefault();
                setError(null);
                set.mutate({ id: appId, data: { passphrase } });
              }}
              className="space-y-4"
            >
              <Field label="Passphrase" qualifier="· at least 8 characters" error={error ?? undefined}>
                {(id) => (
                  <Input
                    id={id}
                    required
                    autoFocus
                    minLength={8}
                    value={passphrase}
                    onChange={(e) => setPassphrase(e.target.value)}
                    className="mono"
                  />
                )}
              </Field>
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
                  state={set.isPending ? "busy" : "idle"}
                  busyLabel="Setting…"
                  disabledReason={passphrase.length < 8 ? "At least 8 characters" : undefined}
                >
                  {enabled ? "Rotate" : "Set passphrase"}
                </ActionButton>
              </div>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
