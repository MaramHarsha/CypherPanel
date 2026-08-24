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
import { toast } from "sonner";
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
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";

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
        toast.success("Ping queued — watch the delivery feed");
      },
      onError: (err: unknown) => toast.error(err instanceof Error ? err.message : "Could not send the ping"),
    },
  });

  const toggle = useUpdateWebhookEndpoint({
    mutation: {
      onSuccess: () => {
        void invalidate();
        toast.success(e.enabled ? "Endpoint paused" : "Endpoint resumed");
      },
      onError: (err: unknown) => toast.error(err instanceof Error ? err.message : "Could not update the endpoint"),
    },
  });

  const rotate = useRotateWebhookSecret({
    mutation: {
      onSuccess: (data) => {
        void invalidate();
        setRotated(data.secret);
      },
      onError: (err: unknown) => toast.error(err instanceof Error ? err.message : "Could not rotate the secret"),
    },
  });

  const del = useDeleteWebhookEndpoint({
    mutation: {
      onSuccess: () => {
        void invalidate();
        toast.success("Endpoint deleted");
      },
      onError: (err: unknown) => toast.error(err instanceof Error ? err.message : "Could not delete the endpoint"),
    },
  });

  return (
    <li className="overflow-hidden rounded-lg border border-border bg-surface">
      <div className="flex items-start justify-between gap-3 px-4 py-3">
        <span className="flex min-w-0 flex-col gap-1">
          <span className="flex items-center gap-2">
            <StatusDot status={HEALTH_STATUS[e.health] ?? "unknown"} />
            <span className="mono truncate text-[13px] text-text" title={e.url}>
              {e.url}
            </span>
            {!e.enabled && <span className="mono shrink-0 text-[11px] text-text-faint">paused</span>}
          </span>
          <span className="flex flex-wrap gap-1.5">
            {e.events.map((ev) => (
              <span key={ev} className="mono rounded border border-border px-1.5 py-px text-[11px] text-text-faint">
                {ev}
              </span>
            ))}
          </span>
        </span>
        <span className="flex shrink-0 items-center gap-1.5">
          <Button size="sm" variant="ghost" disabled={ping.isPending} onClick={() => ping.mutate({ id: e.id })}>
            <Send className="h-3.5 w-3.5" /> Ping
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={toggle.isPending}
            onClick={() => toggle.mutate({ id: e.id, data: { enabled: !e.enabled } })}
          >
            {e.enabled ? "Pause" : "Resume"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            aria-label="Rotate signing secret"
            disabled={rotate.isPending}
            onClick={() => rotate.mutate({ id: e.id })}
          >
            <RotateCw className="h-3.5 w-3.5" />
          </Button>
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
            onConfirm={() => del.mutate({ id: e.id })}
          />
        </span>
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
  const qc = useQueryClient();
  const deliveries = useListWebhookDeliveries(endpointId, { limit: 5 });

  const redeliver = useRedeliver(endpointId, qc);

  return (
    <div className="border-t border-border px-4 py-2.5">
      <PageState
        query={deliveries}
        isEmpty={(d) => d.deliveries.length === 0}
        skeletonRows={2}
        empty={<p className="py-1 text-xs text-text-faint">No deliveries yet.</p>}
      >
        {(page) => (
          <ul className="space-y-1">
            {page.deliveries.map((d) => (
              <DeliveryRow key={d.id} delivery={d} onRedeliver={() => redeliver.mutate({ id: d.id })} pending={redeliver.isPending} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function useRedeliver(endpointId: string, qc: ReturnType<typeof useQueryClient>) {
  return useRedeliverWebhookDelivery({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListWebhookDeliveriesQueryKey(endpointId) });
        toast.success("Queued for redelivery");
      },
      onError: (err: unknown) => toast.error(err instanceof Error ? err.message : "Could not redeliver"),
    },
  });
}

function DeliveryRow({
  delivery: d,
  onRedeliver,
  pending,
}: {
  delivery: WebhookDelivery;
  onRedeliver: () => void;
  pending: boolean;
}) {
  // `pending` here is the delivery's own state, not the mutation's — a delivery
  // still retrying has not failed yet, so it gets the deploying marker rather
  // than an error one.
  const marker = d.status === "succeeded" ? "running" : d.status === "failed" ? "error" : "deploying";

  return (
    <li className="flex items-center justify-between gap-3 py-0.5">
      <span className="flex min-w-0 items-center gap-2">
        <StatusDot status={marker} />
        <span className="mono truncate text-[11.5px] text-text-mid">
          {d.event_type} · {d.resource_name}
          {d.response_status != null && ` · ${d.response_status}`}
          {d.duration_ms != null && ` · ${d.duration_ms}ms`}
          {d.status === "failed" && d.attempt > 1 && ` · ${d.attempt} attempts`}
        </span>
      </span>
      {d.status === "failed" && (
        <Button size="sm" variant="ghost" disabled={pending} onClick={onRedeliver}>
          Redeliver
        </Button>
      )}
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

  const reset = () => {
    setUrl("");
    setEvents(new Set(["deploy.failed"]));
    setError(null);
    setSecret(null);
    setCreatedUrl("");
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
              <Button variant="primary" type="submit" disabled={create.isPending}>
                {create.isPending ? "Adding…" : "Add endpoint"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
