// Application · Storage: the named volumes that outlive a rollout (canvas
// 13g/6g). The application record carries the set, so the tab reads the same
// query the rest of the app does and hands it to the editor.
import { createFileRoute } from "@tanstack/react-router";
import { useGetApplication } from "@/api/gen/applications/applications";
import { PageState } from "@/components/page-state";
import { VolumesEditor } from "@/components/volumes-editor";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/storage")({
  component: StorageTab,
});

function StorageTab() {
  const { projectId, appId } = Route.useParams();
  const app = useGetApplication(appId);

  // The application always exists here; an empty *volume set* is the editor's
  // own empty state, with the add verb in it.
  return (
    <PageState query={app} isEmpty={() => false} skeletonRows={2}>
      {(a) => <VolumesEditor key={a.id} app={a} projectId={projectId} />}
    </PageState>
  );
}
