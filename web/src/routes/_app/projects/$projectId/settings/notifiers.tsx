// Project settings · Notifiers: on these events, deliver to this channel. Config
// is write-only (masked hint after saving); the Test button is first-class —
// a notifier that silently never fires is the common footgun (notifications.md).
//
// The add flow is the one connection pattern of canvas 13aj, shared with the
// registries and log drains that will follow it (components/connection-dialog).
// The masthead and the page gutters belong to settings.tsx, so the tab renders
// only its own section.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { getListNotifiersQueryKey, useDeleteNotifier, useListNotifiers, useTestNotifier } from "@/api/gen/notifiers/notifiers";
import { useGetProject } from "@/api/gen/projects/projects";
import type { Notifier } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { NotifierConnectionDialog } from "@/components/connection-dialog";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/settings/notifiers")({ component: NotifiersTab });

function NotifiersTab() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const notifiers = useListNotifiers(projectId);

  useCrumbs([
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings", to: `/projects/${projectId}/settings` },
    { label: "notifiers" },
  ]);

  const add = (primary?: boolean) => (
    <NotifierConnectionDialog
      projectId={projectId}
      trigger={
        <Button size="sm" variant={primary ? "primary" : "secondary"}>
          <Plus className="h-3.5 w-3.5" /> New notifier
        </Button>
      }
    />
  );

  return (
    <div className="max-w-2xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Notifiers</Eyebrow>
        {add()}
      </div>
      <InlineHint>
        Get a message when a deploy or backup in this project succeeds or fails — on Discord, Slack, Telegram, or email.
      </InlineHint>
      <PageState
        query={notifiers}
        skeletonColumns="1fr auto"
        empty={
          <EmptyState
            title="No notifiers"
            hint="Add one to hear about failures without watching the panel."
            action={add(true)}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((n) => (
              <NotifierRow key={n.id} projectId={projectId} notifier={n} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function NotifierRow({ projectId, notifier: n }: { projectId: string; notifier: Notifier }) {
  const qc = useQueryClient();
  // The API answers 202 once the delivery is attempted; a channel that refused
  // the message is logged server-side, not reported. "Sent" is therefore the
  // honest success word, and the toast says where to look for the real answer.
  const test = useTestNotifier({
    mutation: {
      onSuccess: () =>
        toastSuccess({ title: "Test sent — check the channel", detail: "A channel failure is logged, not surfaced." }),
      onError: (e: unknown, vars) => toastFailed("Test failed to send", e, { retry: () => test.mutate(vars) }),
    },
  });
  const testState = useMutationActionState(test);
  const del = useDeleteNotifier({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListNotifiersQueryKey(projectId) });
        toastSuccess(`Deleted ${n.name}`);
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the notifier", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-3">
      <span className="flex min-w-0 flex-col">
        <span className="flex items-center gap-2">
          <span className="text-[13px] font-medium text-text">{n.name}</span>
          <span className="mono text-[11px] text-text-faint">{n.channel}</span>
          {!n.enabled && <span className="mono text-[11px] text-text-faint">off</span>}
        </span>
        <span className="mono truncate text-xs text-text-faint" title={n.config_hint}>
          {n.config_hint} · {n.events.length} event{n.events.length > 1 ? "s" : ""}
        </span>
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <ActionButton
          size="sm"
          variant="ghost"
          state={testState}
          busyLabel="Sending…"
          successLabel="Sent"
          onClick={() => test.mutate({ id: n.id })}
        >
          ↗ Test
        </ActionButton>
        <NotifierConnectionDialog
          projectId={projectId}
          notifier={n}
          trigger={
            <Button size="sm" variant="ghost">
              Edit
            </Button>
          }
        />
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost" aria-label={`Delete ${n.name}`}>
              <Trash2 className="h-3.5 w-3.5 text-danger" />
            </Button>
          }
          title={`Delete ${n.name}?`}
          blastRadius={["Stops these notifications.", "Nothing else is affected."]}
          actionLabel="Delete notifier"
          pending={del.isPending}
          pendingLabel="Deleting…"
          onConfirm={() => del.mutate({ id: n.id })}
        />
      </span>
    </li>
  );
}
