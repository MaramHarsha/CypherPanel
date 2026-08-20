// Database · Settings: danger zone (typed-name delete, ui-principles §2). The
// backup count is stated in the blast radius so nobody deletes it blind.
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useListDatabaseBackups } from "@/api/gen/backups/backups";
import { useDeleteDatabase, useGetDatabase } from "@/api/gen/databases/databases";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId/settings")({
  component: DatabaseSettings,
});

function DatabaseSettings() {
  const { projectId, dbId } = Route.useParams();
  const navigate = useNavigate();
  const db = useGetDatabase(dbId);
  const schedules = useListDatabaseBackups(dbId);

  const del = useDeleteDatabase({
    mutation: {
      onSuccess: () => {
        toast.success(`Deleted ${db.data?.name ?? "database"}`);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the database"),
    },
  });

  const scheduleCount = schedules.data?.length ?? 0;

  return (
    <PageState query={db} isEmpty={() => false}>
      {(d) => (
        <div className="max-w-xl space-y-2">
          <Eyebrow className="text-danger">Danger zone</Eyebrow>
          <div className="flex items-center justify-between gap-3 rounded-lg border border-danger/35 p-4">
            <div>
              <p className="text-[13px] font-medium text-text">Delete this database</p>
              <p className="text-xs text-text-mid">Stops the container and deletes its data volume. Backup files in your S3 targets are kept.</p>
            </div>
            <ConfirmDestructive
              trigger={<Button variant="danger">Delete</Button>}
              title={`Delete ${d.name}?`}
              // One entry per class of thing, each carrying its own
              // consequence: a run-on sentence about a deletion is the one
              // paragraph an operator skims (canvas 13af).
              blastRadius={[
                `the ${d.engine} container and its data volume — permanently (backup files already in S3 survive)`,
                ...(scheduleCount > 0
                  ? [`${scheduleCount} backup schedule${scheduleCount > 1 ? "s" : ""} — nothing further is uploaded`]
                  : []),
              ]}
              confirmName={d.name}
              actionLabel="Delete database"
              pending={del.isPending}
              onConfirm={() => del.mutate({ id: dbId })}
            />
          </div>
        </div>
      )}
    </PageState>
  );
}
