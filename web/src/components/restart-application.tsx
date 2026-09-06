// Restart, expressed as desired state (deployment-control.md). A fresh restart
// token rides on the AppSpec and is stamped on the container, so the drift the
// existing recreate path already closes is what performs the restart: the
// replacement starts alongside, health-gates, takes the route, and only then
// does the old one drain. That is the whole reason the button can promise zero
// downtime, and the reason it says so before the click rather than after.
//
// It is deliberately NOT a deploy: no revision, no build, no deployment row,
// and the desired revision is unmoved — restarting a wedged container must not
// silently ship an hour-old edit. Nothing to restart is a real state, so a
// stopped application gets the reason rather than a button that would 409.
//
// It is a component of its own because the restart is offered from two places:
// the Overview status card, and — on a phone (canvas 14d) — the foot of the
// Logs screen, where the reason to restart is usually being read.
import { useQueryClient } from "@tanstack/react-query";
import { RotateCw } from "lucide-react";
import { getGetApplicationQueryKey, useRestartApplication } from "@/api/gen/applications/applications";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import type { ButtonProps } from "@/components/ui/button";
import { toastFailed, toastSuccess } from "@/lib/toast";

export function RestartApplicationButton({
  appId,
  status,
  label = "Restart",
  variant = "ghost",
  size = "sm",
  className,
}: {
  appId: string;
  status: string | undefined;
  label?: string;
  variant?: ButtonProps["variant"];
  size?: ButtonProps["size"];
  className?: string;
}) {
  const qc = useQueryClient();
  const restart = useRestartApplication({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetApplicationQueryKey(appId) });
        toastSuccess({
          title: "Restarting",
          detail: "The replacement starts alongside and takes the route once it is healthy.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not restart", e, { retry: () => restart.mutate(vars) }),
    },
  });
  const state = useMutationActionState(restart);
  const idle = status === "stopped" || status === "unknown";

  return (
    <ActionButton
      size={size}
      variant={variant}
      state={state}
      busyLabel="Restarting…"
      successLabel="Restarting"
      failedLabel="Retry"
      disabledReason={idle ? "Nothing is running to restart — deploy it first" : undefined}
      onClick={() => restart.mutate({ id: appId })}
      title="Replaces the container with a fresh one of the same revision"
      className={className}
    >
      <RotateCw className="h-3.5 w-3.5" aria-hidden /> {label}
    </ActionButton>
  );
}
