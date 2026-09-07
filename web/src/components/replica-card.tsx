// Application · Scale (app-scaling.md §§2, 3, 7; canvas 13m's PLACEMENT card).
//
// A replica count is DESIRED STATE, not an action: there is no "scale now"
// button, because the reconciler already knows how to make reality match. The
// stepper writes an integer and the panel then shows the gap between desired
// and running until the node closes it.
//
// Three things are said before the click rather than discovered afterwards:
//
//   - Scaling OUT surges. The new replicas start alongside the old ones and
//     the route flips once they are all healthy, so the node needs headroom
//     for twice the count during a rollout. That arithmetic is printed beside
//     the stepper rather than left to be found through an OOM kill.
//   - Scaling IN drains. The departing replica leaves the route first,
//     in-flight requests finish, and only then is the container stopped.
//   - An application that mounts a volume or publishes a raw host port cannot
//     have more than one replica, and the card says WHICH volume or port —
//     the API refuses it either way, and a refusal you can only discover by
//     trying is a worse control than one you can read.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { getGetApplicationQueryKey, useUpdateApplication } from "@/api/gen/applications/applications";
import type { Application } from "@/api/gen/model";
import { Eyebrow } from "@/components/eyebrow";
import { StatusBadge } from "@/components/status-badge";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

const MAX_REPLICAS = 20;

/** Why this application cannot scale, or null when it can. */
function blockedReason(app: Application): string | null {
  const volume = (app.volumes ?? [])[0];
  if (volume) {
    return `It mounts the volume ${volume.name} at ${volume.path}, so it holds state on disk. Two containers writing one volume is how a database file gets corrupted.`;
  }
  const port = (app.ports ?? [])[0];
  if (port) {
    return `It publishes host port ${port.host_port} directly, and two containers cannot bind one port.`;
  }
  return null;
}

export function ReplicaCard({ app }: { app: Application }) {
  const qc = useQueryClient();
  const desired = app.runtime.replicas || 1;
  const [want, setWant] = useState(desired);
  const blocked = blockedReason(app);
  const observed = app.replicas ?? [];
  const running = observed.filter((r) => r.state === "running").length;

  const save = useUpdateApplication({
    mutation: {
      onSuccess: (_a, vars) => {
        void qc.invalidateQueries({ queryKey: getGetApplicationQueryKey(app.id) });
        const n = vars.data.runtime?.replicas ?? desired;
        toastSuccess({
          title: n > desired ? `Scaling to ${n} replicas` : `Scaling down to ${n}`,
          detail:
            n > desired
              ? "The new containers start alongside and take traffic once they are healthy."
              : "The departing containers leave the route first, then drain.",
        });
      },
      onError: (e: unknown, vars) => {
        setWant(desired);
        toastFailed("Could not change the replica count", e, { retry: () => save.mutate(vars) });
      },
    },
  });

  return (
    <section className="space-y-2.5">
      <Eyebrow>Scale</Eyebrow>
      <div className="space-y-3.5 rounded-lg border border-border bg-surface px-4 py-3.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <p className="text-[13px] font-semibold text-text">Replicas</p>
            <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
              {blocked ? (
                <>This application runs as one container. {blocked}</>
              ) : (
                <>
                  Containers of this application, load-balanced by the Proxy on its node. More of them means multi-core
                  use for a single-threaded runtime, and one container dying stops being an outage.
                </>
              )}
            </p>
          </div>
          {!blocked && (
            <div className="flex shrink-0 items-center gap-1.5">
              <Button
                size="sm"
                variant="secondary"
                aria-label="One fewer replica"
                disabledReason={want <= 1 ? "One is the minimum" : undefined}
                onClick={() => setWant(want - 1)}
              >
                −
              </Button>
              <span className="mono w-8 text-center text-[15px] font-medium text-text">{want}</span>
              <Button
                size="sm"
                variant="secondary"
                aria-label="One more replica"
                disabledReason={want >= MAX_REPLICAS ? `${MAX_REPLICAS} is the maximum` : undefined}
                onClick={() => setWant(want + 1)}
              >
                +
              </Button>
            </div>
          )}
        </div>

        {!blocked && want !== desired && (
          <div className="space-y-2 border-t border-border-subtle pt-3">
            {want > desired && (
              // The arithmetic, before the click.
              <p className="text-[12px] leading-[1.5] text-text-mid">
                During the next deploy this node runs up to <strong className="font-medium text-text">{want * 2}</strong>{" "}
                containers of this application at once — the new set starts alongside the old before the route moves.
                {app.runtime.memory_limit_mb
                  ? ` With a ${app.runtime.memory_limit_mb} MiB limit each, that is ${want * 2 * app.runtime.memory_limit_mb} MiB at the peak.`
                  : " Give the host headroom for that."}
              </p>
            )}
            {want < desired && (
              <p className="text-[12px] leading-[1.5] text-text-mid">
                {desired - want === 1 ? "One replica" : `${desired - want} replicas`} will leave the route, finish
                in-flight requests, and then stop. No request is dropped.
              </p>
            )}
            <div className="flex items-center justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setWant(desired)}>
                Cancel
              </Button>
              <ActionButton
                size="sm"
                variant="secondary"
                state={save.isPending ? "busy" : "idle"}
                busyLabel="Saving…"
                onClick={() => save.mutate({ id: app.id, data: { runtime: { replicas: want } } })}
              >
                Set to {want}
              </ActionButton>
            </div>
          </div>
        )}

        {/* The gap between desired and running, which is the only interesting
            thing about a replica set. Drawn only once there is more than one:
            for a single-replica application the status badge above already
            says everything. */}
        {desired > 1 && (
          <div className="space-y-2 border-t border-border-subtle pt-3">
            <p className="mono text-[11.5px] text-text-faint">
              desired {desired} · running {running}
            </p>
            <ul className="grid gap-1.5 sm:grid-cols-2">
              {Array.from({ length: desired }, (_, i) => i + 1).map((idx) => {
                const r = observed.find((o) => o.index === idx);
                return (
                  <li
                    key={idx}
                    className={cn(
                      "flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5",
                      r?.state === "running" ? "border-border" : "border-border-subtle",
                    )}
                  >
                    <span className="mono text-[12px] text-text">
                      {app.name}-{idx}
                    </span>
                    {r ? (
                      <StatusBadge status={r.state} />
                    ) : (
                      // Never zero, never green: an index the node has not
                      // reported is unknown, and saying so is the point.
                      <span className="mono text-[11px] text-text-faint">not reported</span>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </div>
    </section>
  );
}
