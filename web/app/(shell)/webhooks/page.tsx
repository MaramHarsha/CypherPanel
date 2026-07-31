"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Plus, RefreshCw, Trash2, Webhook as WebhookIcon } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { PageHeader } from "@/components/page-header";
import {
  ApiError,
  createWebhook,
  deleteWebhook,
  listWebhookDeliveries,
  listWebhooks,
  redeliverWebhook,
  setWebhookActive,
  webhookEventSubjects,
  type Webhook,
  type WebhookDelivery,
} from "@/lib/api";

function deliveryVariant(status: WebhookDelivery["status"]) {
  if (status === "delivered") return "success" as const;
  if (status === "dead") return "destructive" as const;
  return "secondary" as const;
}

// The signing key exists in plaintext exactly once — in the create response.
// Surfacing it in a dismissible panel (rather than a toast that can vanish
// mid-copy) is the difference between a usable integration and a deleted one.
function SecretPanel({ secret, onDismiss }: { secret: string; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <Card className="mt-4 border-primary/40">
      <CardContent className="p-4">
        <p className="font-medium">Signing key — copy it now</p>
        <p className="mt-1 text-sm text-muted-foreground">
          This is the only time this key is shown. Your endpoint needs it to verify the{" "}
          <span className="font-mono">X-CypherPanel-Signature</span> header. If you lose it,
          delete the endpoint and create a new one.
        </p>
        <div className="mt-3 flex items-center gap-2">
          <code className="flex-1 truncate rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {secret}
          </code>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              navigator.clipboard.writeText(secret).then(() => setCopied(true));
            }}
          >
            <Copy className="h-4 w-4" /> {copied ? "Copied" : "Copy"}
          </Button>
          <Button variant="ghost" size="sm" onClick={onDismiss}>
            Done
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function DeleteWebhookButton({ hook }: { hook: Webhook }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteWebhook(hook.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["webhooks"] });
      qc.invalidateQueries({ queryKey: ["webhook-deliveries"] });
      setOpen(false);
    },
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Delete endpoint ${hook.name}`}
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-4 w-4 text-destructive" />
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {hook.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the endpoint, its signing key, and its entire delivery history.
            Events already queued for it will not be sent.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={remove.isPending}
            onClick={(e) => {
              e.preventDefault();
              remove.mutate();
            }}
          >
            {remove.isPending ? "Deleting…" : "Delete"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function WebhooksPage() {
  const qc = useQueryClient();
  const [form, setForm] = useState({ name: "", url: "" });
  const [selected, setSelected] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [newSecret, setNewSecret] = useState<string | null>(null);

  const { data: hooks, isLoading } = useQuery({ queryKey: ["webhooks"], queryFn: listWebhooks });
  const { data: subjects } = useQuery({
    queryKey: ["webhook-event-subjects"],
    queryFn: webhookEventSubjects,
  });
  // Deliveries move through pending → delivered/failed in the background, so
  // poll while the page is open.
  const { data: deliveries } = useQuery({
    queryKey: ["webhook-deliveries"],
    queryFn: () => listWebhookDeliveries("", 50),
    refetchInterval: 5000,
  });

  const create = useMutation({
    mutationFn: () => createWebhook({ name: form.name, url: form.url, events: selected }),
    onSuccess: (hook) => {
      qc.invalidateQueries({ queryKey: ["webhooks"] });
      setForm({ name: "", url: "" });
      setSelected([]);
      setError(null);
      if (hook.secret) setNewSecret(hook.secret);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not create endpoint"),
  });

  const toggle = useMutation({
    mutationFn: ({ id, active }: { id: string; active: boolean }) => setWebhookActive(id, active),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["webhooks"] }),
  });

  const redeliver = useMutation({
    mutationFn: (id: string) => redeliverWebhook(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["webhook-deliveries"] }),
  });

  const canCreate = form.name.trim() !== "" && form.url.trim() !== "" && !create.isPending;

  return (
    <div>
      <PageHeader
        title="Webhooks"
        description="Deliver domain events to your own systems, signed with HMAC-SHA256. Failed deliveries retry with backoff and can be replayed."
      />

      <Card>
        <CardContent className="grid gap-3 p-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="hook-name">Name</Label>
              <Input
                id="hook-name"
                placeholder="billing-sync"
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="hook-url">Endpoint URL</Label>
              <Input
                id="hook-url"
                placeholder="https://billing.example.com/hooks/cypherpanel"
                value={form.url}
                onChange={(e) => setForm((f) => ({ ...f, url: e.target.value }))}
              />
            </div>
          </div>

          <fieldset className="grid gap-2">
            <legend className="text-sm font-medium">
              Events{" "}
              <span className="font-normal text-muted-foreground">
                (none selected = every event)
              </span>
            </legend>
            <div className="flex flex-wrap gap-2">
              {(subjects ?? []).map((s) => {
                const on = selected.includes(s);
                return (
                  <Button
                    key={s}
                    type="button"
                    variant={on ? "default" : "outline"}
                    size="sm"
                    aria-pressed={on}
                    onClick={() =>
                      setSelected((prev) =>
                        prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s],
                      )
                    }
                  >
                    {s.replace("events.", "")}
                  </Button>
                );
              })}
            </div>
          </fieldset>

          <Button onClick={() => create.mutate()} disabled={!canCreate}>
            <Plus className="h-4 w-4" /> Add endpoint
          </Button>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </CardContent>
      </Card>

      {newSecret && <SecretPanel secret={newSecret} onDismiss={() => setNewSecret(null)} />}

      <Card className="mt-4">
        <CardContent className="p-0">
          {isLoading ? (
            <div className="space-y-2 p-4">
              {[0, 1].map((i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : (hooks ?? []).length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              <WebhookIcon className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
              No endpoints yet.
            </p>
          ) : (
            <div className="divide-y divide-border">
              {(hooks ?? []).map((h) => (
                <div key={h.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                  <div className="min-w-0">
                    <p className="truncate font-medium">{h.name}</p>
                    <p className="truncate font-mono text-xs text-muted-foreground">{h.url}</p>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      {h.events.length === 0
                        ? "all events"
                        : h.events.map((e) => e.replace("events.", "")).join(", ")}
                    </p>
                  </div>
                  <div className="flex items-center gap-3">
                    <Switch
                      checked={h.active}
                      aria-label={`${h.active ? "Disable" : "Enable"} ${h.name}`}
                      onCheckedChange={(active) => toggle.mutate({ id: h.id, active })}
                    />
                    <DeleteWebhookButton hook={h} />
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <h2 className="mt-8 mb-3 text-lg font-medium">Delivery log</h2>
      <Card>
        <CardContent className="p-0">
          {(deliveries ?? []).length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No deliveries yet.
            </p>
          ) : (
            <div className="divide-y divide-border">
              {(deliveries ?? []).map((d) => (
                <div key={d.id} className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
                  <div className="min-w-0">
                    <p className="truncate text-sm">
                      <span className="font-mono">{d.subject.replace("events.", "")}</span>
                      <span className="ml-2 text-muted-foreground">→ {d.webhook_name}</span>
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {new Date(d.created_at).toLocaleString()} · attempt {d.attempts}
                      {d.response_status > 0 && ` · HTTP ${d.response_status}`}
                    </p>
                    {d.error && <p className="mt-0.5 truncate text-xs text-destructive">{d.error}</p>}
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge variant={deliveryVariant(d.status)}>{d.status}</Badge>
                    {(d.status === "dead" || d.status === "failed") && (
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`Redeliver ${d.subject} to ${d.webhook_name}`}
                        onClick={() => redeliver.mutate(d.id)}
                        disabled={redeliver.isPending}
                      >
                        <RefreshCw className="h-4 w-4" /> Redeliver
                      </Button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
