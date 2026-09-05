// Project settings · Webhooks: notifiers talk to people, webhooks talk to
// machines (canvas 14h, outbound-webhooks.md §7). The two live as sibling tabs
// because the audiences want opposite things — prose in a chat channel versus a
// signed JSON contract a receiver can verify.
//
// Three things the board insists on, and the reasons they are shaped this way:
//
//   · the URL *is* the endpoint's identity — there is no name field, so the row
//     leads with the receiver and the health marker rather than a label someone
//     has to keep truthful;
//   · the signing secret is shown exactly once, on create and on rotate. It is
//     never retrievable, so it gets the full one-time panel rather than a toast
//     that can be dismissed before it is read;
//   · the delivery feed is the honest half. An endpoint that silently stopped
//     firing is the failure mode worth designing for, so every attempt is
//     listed with its response and a redeliver button.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, RotateCw, Send, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  getListWebhookDeliveriesQueryKey,
  getListWebhookEndpointsQueryKey,
  useCreateWebhookEndpoint,
  useDeleteWebhookEndpoint,
  useListWebhookDeliveries,
  useListWebhookEndpoints,
  usePingWebhookEndpoint,
  useRedeliverWebhookDelivery,
  useRotateWebhookSecret,
  useUpdateWebhookEndpoint,
} from "@/api/gen/webhooks/webhooks";
import { useGetProject } from "@/api/gen/projects/projects";
import type { WebhookDelivery, WebhookEndpoint } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyField, SecretField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { StatusDot, StatusWord } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { absoluteTime, relativeTime, timeUntil } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/settings/webhooks")({
  component: WebhooksTab,
});

/** The board's four subscribable events, in the order it lists them. */
const EVENTS = [
  { key: "deploy.succeeded", label: "Deploy succeeded" },
  { key: "deploy.failed", label: "Deploy failed" },
  { key: "backup.succeeded", label: "Backup succeeded" },
  { key: "backup.failed", label: "Backup failed" },
] as const;

/** Endpoint health maps onto the shared status vocabulary rather than inventing
 *  a second one — `failing` is an error (square marker), `unknown` stays hollow
 *  so a brand-new endpoint never fakes certainty (ui-principles §5). */
const HEALTH_STATUS: Record<string, string> = {
  healthy: "running",
  degraded: "degraded",
  failing: "error",
  unknown: "unknown",
};

function WebhooksTab() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const endpoints = useListWebhookEndpoints(projectId);

  useCrumbs([
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings", to: `/projects/${projectId}/settings` },
    { label: "webhooks" },
  ]);

  return (
    <div className="max-w-2xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Webhooks</Eyebrow>
        <NewEndpointDialog projectId={projectId} />
      </div>
      <InlineHint>
        Notifiers talk to people; webhooks talk to your systems. Each endpoint is signed with its own secret, shown once.
      </InlineHint>
      <PageState
        query={endpoints}
        empty={
          <EmptyState
            title="No endpoints"
            hint="Add one to push deploy and backup events into your own tooling."
            action={<NewEndpointDialog projectId={projectId} primary />}
          />
        }
      >
        {(list) => (
          <ul className="space-y-2.5">
            {list.map((e) => (
              <EndpointCard key={e.id} projectId={projectId} endpoint={e} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function EndpointCard({ projectId, endpoint: e }: { projectId: string; endpoint: WebhookEndpoint }) {
  const qc = useQueryClient();
  const [rotated, setRotated] = useState<string | null>(null);
  const invalidate = () => qc.invalidateQueries({ queryKey: getListWebhookEndpointsQueryKey(projectId) });

  const ping = usePingWebhookEndpoint({
    mutation: {
      onSuccess: () => {
        // A ping becomes a delivery row, so the feed below has to refetch or
        // the button appears to have done nothing.
        void qc.invalidateQueries({ queryKey: getListWebhookDeliveriesQueryKey(e.id) });
        void invalidate();
        toastSuccess("Ping queued — watch the delivery feed");
      },
      onError: (err: unknown, vars) => toastFailed("Could not send the ping", err, { retry: () => ping.mutate(vars) }),
    },
  });

  const toggle = useUpdateWebhookEndpoint({
    mutation: {
      onSuccess: () => {
        void invalidate();
        toastSuccess(e.enabled ? "Endpoint paused" : "Endpoint resumed");
      },
      onError: (err: unknown, vars) => toastFailed("Could not update the endpoint", err, { retry: () => toggle.mutate(vars) }),
    },
  });

  const rotate = useRotateWebhookSecret({
    mutation: {
      onSuccess: (data) => {
        void invalidate();
        setRotated(data.secret);
      },
      onError: (err: unknown, vars) => toastFailed("Could not rotate the secret", err, { retry: () => rotate.mutate(vars) }),
    },
  });

  const del = useDeleteWebhookEndpoint({
    mutation: {
      onSuccess: () => {
        void invalidate();
        toastSuccess("Endpoint deleted");
      },
      onError: (err: unknown, vars) => toastFailed("Could not delete the endpoint", err, { retry: () => del.mutate(vars) }),
    },
  });

  const pingState = useMutationActionState(ping);
  const toggleState = useMutationActionState(toggle);
  const rotateState = useMutationActionState(rotate);
  const health = HEALTH_STATUS[e.health] ?? "unknown";

  return (
    <li className="overflow-hidden rounded-lg border border-border bg-surface">
      <div className="px-4 pb-3 pt-[13px]">
        {/* 14h's card head: the receiver, then its health as a word at the
            far right — `● HEALTHY` — so the state is readable, not only a
            dot with an aria-label. */}
        <div className="flex items-center gap-2.5">
          <span className="mono min-w-0 flex-1 truncate text-[12.5px] font-medium text-text" title={e.url}>
            {e.url}
          </span>
          {!e.enabled && <span className="mono shrink-0 text-[11px] text-text-faint">paused</span>}
          <span className="flex shrink-0 items-center gap-1.5">
            <StatusDot status={health} className="h-2 w-2" />
            <StatusWord status={health} className="text-[10.5px]">
              {e.health}
            </StatusWord>
          </span>
        </div>
        <div className="mt-[7px] flex flex-wrap items-center gap-1.5">
          {e.events.map((ev) => (
            <span key={ev} className="mono rounded bg-raised px-2 py-[2px] text-[10.5px] text-text">
              {ev}
            </span>
          ))}
          <span className="ml-auto flex shrink-0 items-center gap-1">
            <ActionButton
              size="sm"
              variant="ghost"
              state={pingState}
              busyLabel="Pinging…"
              successLabel="Queued"
              disabledReason={e.enabled ? undefined : "Resume the endpoint first — a paused endpoint delivers nothing"}
              onClick={() => ping.mutate({ id: e.id })}
            >
              <Send className="h-3.5 w-3.5" /> Ping
            </ActionButton>
            <ActionButton
              size="sm"
              variant="ghost"
              state={toggleState}
              busyLabel={e.enabled ? "Pausing…" : "Resuming…"}
              successLabel={e.enabled ? "Paused" : "Resumed"}
              onClick={() => toggle.mutate({ id: e.id, data: { enabled: !e.enabled } })}
            >
              {e.enabled ? "Pause" : "Resume"}
            </ActionButton>
            <ActionButton
              size="sm"
              variant="ghost"
              state={rotateState}
              busyLabel="Rotating…"
              successLabel="Rotated"
              aria-label="Rotate signing secret"
              onClick={() => rotate.mutate({ id: e.id })}
            >
              <RotateCw className="h-3.5 w-3.5" /> Rotate
            </ActionButton>
            <ConfirmDestructive
              trigger={
                <Button size="sm" variant="ghost" aria-label={`Delete ${e.url}`}>
                  <Trash2 className="h-3.5 w-3.5 text-danger" />
                </Button>
              }
              title="Delete this endpoint?"
              blastRadius={[
                "Stops every future delivery to this receiver.",
                "Removes its delivery history — the feed below is gone with it.",
                "The signing secret is destroyed; a new endpoint gets a new one.",
              ]}
              actionLabel="Delete endpoint"
              pending={del.isPending}
              pendingLabel="Deleting…"
              onConfirm={() => del.mutate({ id: e.id })}
            />
          </span>
        </div>
      </div>

      {rotated && (
        <div className="border-t border-border bg-surface-sunken px-4 py-3">
          <OneTimeSecret
            secret={rotated}
            lead="New signing secret — the old one stopped working the moment you rotated."
            onDismiss={() => setRotated(null)}
          />
        </div>
      )}

      <DeliveryFeed endpointId={e.id} />
    </li>
  );
}

/** The delivery log. Kept inline under its endpoint rather than behind a click:
 *  the board shows it as the endpoint's own evidence, and a feed you have to go
 *  looking for is a feed nobody checks. */
function DeliveryFeed({ endpointId }: { endpointId: string }) {
  const deliveries = useListWebhookDeliveries(endpointId, { limit: 5 });

  return (
    // The feed sits on the page ground, sunk below the white card head (14h):
    // the endpoint is the object, the log is its evidence.
    <div className="border-t border-border-subtle bg-bg px-4 pb-2.5 pt-1.5">
      <PageState
        query={deliveries}
        isEmpty={(d) => d.deliveries.length === 0}
        skeletonRows={2}
        empty={
          // Nested evidence, not a page: no glyph, and the verb it points at
          // — Ping — is on the card head above it.
          <EmptyState
            glyph={null}
            className="py-2"
            title="No deliveries yet"
            hint="Ping sends a signed test event now; the next deploy or backup in this project follows on its own."
          />
        }
      >
        {(page) => (
          <ul className="divide-y divide-border-subtle">
            {page.deliveries.map((d) => (
              <DeliveryRow key={d.id} endpointId={endpointId} delivery={d} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

/** One attempt log line — `● deploy.succeeded · web · 200 · 84ms   2m ago`. */
function DeliveryRow({ endpointId, delivery: d }: { endpointId: string; delivery: WebhookDelivery }) {
  const qc = useQueryClient();
  // Held per row so the pill that was pressed is the one that reports (10b);
  // a feed-wide mutation would spin every redeliver button at once.
  const redeliver = useRedeliverWebhookDelivery({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListWebhookDeliveriesQueryKey(endpointId) });
        toastSuccess("Queued for redelivery");
      },
      onError: (err: unknown, vars) => toastFailed("Could not redeliver", err, { retry: () => redeliver.mutate(vars) }),
    },
  });
  const redeliverState = useMutationActionState(redeliver);

  // A delivery past its first attempt has already failed once, whatever its
  // status says now — it gets the square (14h draws `■ … retrying ×3`), and
  // a redeliver button, because waiting out the backoff is not the only
  // option. A first attempt still in flight is the only row that pulses.
  const retrying = d.status === "pending" && d.attempt > 1;
  const marker = d.status === "succeeded" ? "running" : d.status === "failed" || retrying ? "error" : "deploying";
  const canRedeliver = d.status === "failed" || retrying;

  return (
    <li className="flex items-center gap-2.5 py-[7px]">
      <StatusDot status={marker} className="h-2 w-2" />
      <span className="mono min-w-0 flex-1 truncate text-[11.5px] text-text-dim">
        {d.event_type} · {d.resource_name}
        {d.response_status != null && ` · ${d.response_status}`}
        {d.duration_ms != null && ` · ${d.duration_ms}ms`}
        {retrying && ` · retrying ×${d.attempt}`}
        {retrying && d.next_attempt_at && ` · next ${timeUntil(d.next_attempt_at)}`}
        {d.status === "failed" && d.attempt > 1 && ` · gave up after ${d.attempt} attempts`}
      </span>
      {canRedeliver && (
        <ActionButton
          size="sm"
          variant="secondary"
          state={redeliverState}
          busyLabel="Queueing…"
          successLabel="Queued"
          // 14h's `redeliver` is a small 1px box, not the 1.5px ink pill — the
          // action belongs to one log line, not to the endpoint.
          className="h-[22px] rounded-[5px] border border-border-input bg-surface px-2.5 text-[10.5px] font-normal text-text hover:bg-raised"
          onClick={() => redeliver.mutate({ id: d.id })}
        >
          redeliver
        </ActionButton>
      )}
      <span className="mono shrink-0 text-[11.5px] text-text-faint" title={absoluteTime(d.created_at)}>
        {relativeTime(d.created_at)}
      </span>
    </li>
  );
}

/** The one-time secret panel. Shown on create and on rotate; there is no second
 *  chance to read it, so it does not auto-dismiss and it says so plainly. */
function OneTimeSecret({ secret, lead, onDismiss }: { secret: string; lead: string; onDismiss?: () => void }) {
  return (
    <div className="space-y-2">
      <p className="text-xs leading-relaxed text-text-mid">{lead}</p>
      <SecretField value={secret} />
      <p className="text-xs leading-relaxed text-text-faint">
        Sign the raw request body with HMAC-SHA256 using this secret and compare in constant time.
      </p>
      {onDismiss && (
        <Button size="sm" variant="ghost" onClick={onDismiss}>
          I've stored it
        </Button>
      )}
    </div>
  );
}

function NewEndpointDialog({ projectId, primary }: { projectId: string; primary?: boolean }) {
  const qc = useQueryClient();
  const [url, setUrl] = useState("");
  const [events, setEvents] = useState<Set<string>>(new Set(["deploy.failed"]));
  const [error, setError] = useState<string | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const [createdUrl, setCreatedUrl] = useState("");

  const create = useCreateWebhookEndpoint({
    mutation: {
      onSuccess: (data) => {
        void qc.invalidateQueries({ queryKey: getListWebhookEndpointsQueryKey(projectId) });
        setError(null);
        setCreatedUrl(data.endpoint.url);
        setSecret(data.secret);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not add the endpoint"),
    },
  });

  const addState = useMutationActionState(create);

  const reset = () => {
    setUrl("");
    setEvents(new Set(["deploy.failed"]));
    setError(null);
    setSecret(null);
    setCreatedUrl("");
    create.reset();
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (events.size === 0) {
      setError("Pick at least one event to send");
      return;
    }
    create.mutate({ id: projectId, data: { url, events: [...events] as never[] } });
  };

  return (
    <Dialog onOpenChange={(open) => !open && reset()}>
      <DialogTrigger asChild>
        <Button size="sm" variant={primary ? "primary" : "secondary"}>
          <Plus className="h-3.5 w-3.5" /> Add endpoint
        </Button>
      </DialogTrigger>
      <DialogContent title={secret ? "Endpoint added" : "Add an endpoint"}>
        {secret ? (
          // Success is a different screen, not a toast: the secret is the whole
          // point of the interaction and it can never be shown again.
          <div className="space-y-3">
            <CopyField value={createdUrl} />
            <OneTimeSecret secret={secret} lead="Signing secret — copy it now. It is never shown again." />
            <DialogClose asChild>
              <Button variant="primary">Done</Button>
            </DialogClose>
          </div>
        ) : (
          <form onSubmit={submit} className="space-y-3">
            <Field label="Endpoint URL" hint="Where the signed JSON POST is delivered.">
              {(id) => (
                <Input
                  id={id}
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://ops.example.com/hooks/cypher"
                  className="mono"
                  type="url"
                  required
                  autoFocus
                />
              )}
            </Field>
            <Field label="Events" hint="Which observed outcomes are delivered.">
              {() => (
                <div className="space-y-1.5">
                {EVENTS.map((ev) => (
                  <label key={ev.key} className="flex items-center gap-2 text-[13px] text-text-mid">
                    <input
                      type="checkbox"
                      checked={events.has(ev.key)}
                      onChange={(e) =>
                        setEvents((s) => {
                          const next = new Set(s);
                          if (e.target.checked) next.add(ev.key);
                          else next.delete(ev.key);
                          return next;
                        })
                      }
                    />
                    <span>{ev.label}</span>
                    <span className="mono text-[11px] text-text-faint">{ev.key}</span>
                  </label>
                  ))}
                </div>
              )}
            </Field>
            {error && (
              <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
                {error}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button variant="ghost" type="button">
                  Cancel
                </Button>
              </DialogClose>
              <ActionButton variant="primary" type="submit" state={addState} busyLabel="Adding…" successLabel="Added">
                Add endpoint
              </ActionButton>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
