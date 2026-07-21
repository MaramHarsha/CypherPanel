// Application · Logs: runtime tail, replay-then-tail with a reconnect banner
// (web-ui-design.md §4).
import { createFileRoute } from "@tanstack/react-router";
import { getStreamApplicationLogsUrl } from "@/api/gen/applications/applications";
import { LogViewer } from "@/components/log-viewer";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/logs")({
  component: LogsTab,
});

function LogsTab() {
  const { appId } = Route.useParams();
  return <LogViewer url={getStreamApplicationLogsUrl(appId)} className="h-[calc(100dvh-16rem)]" />;
}
