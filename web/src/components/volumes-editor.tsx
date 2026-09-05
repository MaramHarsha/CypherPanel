// VolumesEditor — design canvas 13g (dark twin of 6g): the application's
// Storage tab. Named volumes are the one thing in a container that outlives a
// rollout, so the tab says exactly that and then lists them: name in mono, the
// mount path under it, a ✕ at the right.
//
// The API replaces the volume set wholesale (PatchApplicationRequest.volumes),
// so add and remove are both "send the whole list". Nothing here touches the
// data: the agent only EnsureVolume's on container create and never reclaims
// (application-deploy.md §5, "Persistent volumes"), so removing a row detaches
// the mount on the next deploy and leaves the Docker volume on the server. The
// confirm says so — a "permanently removes" that does not remove would be the
// wrong kind of surprise.
//
// Not drawn, because the plane cannot see them: the canvas's per-volume size
// ("3.2 GB") and the "in backups ✓" chip. AppVolume is {name, path}; volume
// backups have no endpoint and no spec (feature-matrix.md, V1.x).
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { getGetApplicationQueryKey, useUpdateApplication } from "@/api/gen/applications/applications";
import { getListDeploymentsQueryKey, useDeployApplication, useListDeployments } from "@/api/gen/deployments/deployments";
import type { AppVolume, Application } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { RedeployPending } from "@/components/redeploy-pending";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

// The server's own rules (core/applications validateVolumes), checked here so
// the operator keeps their typing instead of bouncing off a toast.
const NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;
const MAX_VOLUMES = 20;

const INTRO =
  "Named volumes survive deploys and restarts. Everything else in the container is thrown away on every rollout.";

export function VolumesEditor({ app, projectId }: { app: Application; projectId: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const volumes = app.volumes ?? [];

  // "Applies on the next deploy" is a fact about time, not about the record:
  // the plane stores the new set at once, and the container gets it when it
  // is next replaced. So the badge is armed by a save here and stands down
  // when a deployment newer than that save appears — the same list the
  // masthead's Deploy pill already watches.
  const [changedAt, setChangedAt] = useState<number | null>(null);
  const deployments = useListDeployments(app.id);
  const redeployPending =
    changedAt !== null && !(deployments.data ?? []).some((d) => Date.parse(d.created_at) > changedAt);

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) => {
        void qc.invalidateQueries({ queryKey: getListDeploymentsQueryKey(app.id) });
        void navigate({
          to: "/projects/$projectId/applications/$appId/deployments",
          params: { projectId, appId: app.id },
          search: { dep: d.id },
        });
      },
      onError: (e: unknown, vars) => toastFailed("Deploy failed to start", e, { retry: () => deploy.mutate(vars) }),
    },
  });
  const applied = {
    detail: "Applies on the next deploy",
    actions: [{ label: "Deploy now", onClick: () => deploy.mutate({ id: app.id, data: {} }) }],
  };

  const update = useUpdateApplication({
    mutation: {
      onSuccess: (_a, vars) => {
        void qc.invalidateQueries({ queryKey: getGetApplicationQueryKey(app.id) });
        setChangedAt(Date.now());
        const grew = (vars.data.volumes?.length ?? 0) > volumes.length;
        toastSuccess({ title: grew ? "Volume added" : "Volume removed", ...applied });
      },
      onError: (e: unknown, vars) =>
        toastFailed("Could not save the volumes", e, { retry: () => update.mutate(vars) }),
    },
  });

  const save = (next: AppVolume[]) => update.mutate({ id: app.id, data: { volumes: next } });

  const addButton = (primary?: boolean) => (
    <AddVolumeDialog
      existing={volumes}
      pending={update.isPending}
      onAdd={(v) => save([...volumes, v])}
      primary={primary}
    />
  );

  return (
    <div className="max-w-xl space-y-3.5">
      <div className="flex items-center gap-3">
        <Eyebrow>Storage</Eyebrow>
        {redeployPending && (
          <RedeployPending title="The volume set changed after the last deploy. Deploy to apply it." />
        )}
        <span className="ml-auto">{addButton()}</span>
      </div>
      <p className="text-[12.5px] leading-relaxed text-text-mid">{INTRO}</p>

      {volumes.length === 0 ? (
        <div className="rounded-lg border border-border bg-surface">
          <EmptyState
            glyph="▤"
            title="No volumes yet"
            hint="Add one to keep data across deploys — uploads, a cache, an SQLite file. Anything written elsewhere in the container is gone on the next rollout."
            action={addButton(true)}
          />
        </div>
      ) : (
        <ul className="overflow-hidden rounded-lg border border-border bg-surface">
          {volumes.map((v) => (
            <li
              key={v.name}
              className="flex items-center gap-3 border-b border-border-subtle px-4 py-[13px] last:border-b-0"
            >
              <div className="min-w-0 flex-1">
                <span className="font-mono text-[12.5px] font-medium text-text">{v.name}</span>
                <div className="mt-[3px] truncate font-mono text-[11.5px] text-text-faint" title={v.path}>
                  → {v.path}
                </div>
              </div>
              <ConfirmDestructive
                trigger={
                  <Button size="sm" variant="ghost" aria-label={`Remove ${v.name}`} className="-mr-2 px-2 text-danger">
                    ✕
                  </Button>
                }
                title={`Remove volume ${v.name}?`}
                lead="Removing this volume:"
                blastRadius={[
                  `the mount at ${v.path} is detached on the next deploy`,
                  "its data stays on the server — nothing is deleted, and reclaiming the Docker volume is a manual step",
                ]}
                confirmName={v.name}
                actionLabel="Remove volume"
                pendingLabel="Removing…"
                pending={update.isPending}
                onConfirm={() => save(volumes.filter((x) => x.name !== v.name))}
              />
            </li>
          ))}
        </ul>
      )}

      <p className="text-[12px] leading-relaxed text-text-faint">
        Removing a volume detaches its mount on the next deploy; the data stays on the server until someone reclaims
        the Docker volume by hand.
      </p>
    </div>
  );
}

function AddVolumeDialog({
  existing,
  pending,
  onAdd,
  primary,
}: {
  existing: AppVolume[];
  pending: boolean;
  onAdd: (v: AppVolume) => void;
  primary?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [error, setError] = useState<string | null>(null);
  const full = existing.length >= MAX_VOLUMES;

  const reset = () => {
    setName("");
    setPath("");
    setError(null);
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const n = name.trim();
    const p = path.trim();
    if (!NAME.test(n)) {
      setError("The name is lowercase letters, digits and dashes — it becomes the Docker volume's name.");
      return;
    }
    if (!p.startsWith("/") || p.includes("..")) {
      setError("The mount path is absolute — it starts with / and has no “..” in it.");
      return;
    }
    if (existing.some((v) => v.name === n)) {
      setError(`A volume named ${n} is already mounted.`);
      return;
    }
    if (existing.some((v) => v.path === p)) {
      setError(`Something is already mounted at ${p}.`);
      return;
    }
    setError(null);
    onAdd({ name: n, path: p });
    setOpen(false);
    reset();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant={primary ? "primary" : "secondary"}
          size={primary ? "lg" : "sm"}
          disabledReason={full ? `An application holds at most ${MAX_VOLUMES} volumes` : undefined}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden /> Add volume
        </Button>
      </DialogTrigger>
      <DialogContent title="Add volume" description="A named volume, mounted into the container at the path you give.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" hint="Lowercase letters, digits and dashes. The Docker volume's name is derived from it.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                required
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="uploads"
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </Field>
          <Field label="Mount path" hint="Where it appears inside the container — absolute, e.g. /data/uploads.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                required
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder="/data/uploads"
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </Field>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2.5">
            <span className="mr-auto text-[11.5px] text-text-faint">mounted on the next deploy</span>
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <ActionButton type="submit" variant="primary" state={pending ? "busy" : "idle"} busyLabel="Adding…">
              Add volume
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
