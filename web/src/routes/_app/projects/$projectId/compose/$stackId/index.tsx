// Compose stack · Overview: the facts, the file a deploy would ship, and the
// one verb that ships it.
//
// A stack is deliberately not a Deployment (compose-stacks.md): there is no
// build and no distribute stage, so there is no pipeline to draw and no
// deployment row to open. Deploying mints a revision and re-points desired
// state, which means the honest thing to show here is the FILE — what would be
// shipped — beside what is currently running. When those two are the same
// revision the page says so; when they are not, the Deploy verb is the thing
// that closes the gap, and it says which direction it is closing.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Rocket } from "lucide-react";
import {
  getGetComposeStackQueryKey,
  getListComposeRevisionsQueryKey,
  useDeployComposeStack,
  useGetComposeFile,
  useGetComposeStack,
} from "@/api/gen/compose-stacks/compose-stacks";
import { useListServers } from "@/api/gen/servers/servers";
import { Fact, FactCard } from "@/components/fact-card";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId/")({
  component: ComposeOverview,
});

/** Short revision for display, as everywhere else a revision id is shown. */
function shortRevision(id: string | null | undefined): string {
  if (!id) return "";
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

function ComposeOverview() {
  const { projectId, stackId } = Route.useParams();
  const qc = useQueryClient();
  const stack = useGetComposeStack(stackId);
  const file = useGetComposeFile(stackId);
  const servers = useListServers();

  const deploy = useDeployComposeStack({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetComposeStackQueryKey(stackId) });
        void qc.invalidateQueries({ queryKey: getListComposeRevisionsQueryKey(stackId) });
        // `up -d` is a convergence, not a request the panel can watch land —
        // the agent reports back through the status on the masthead — so the
        // toast points at the one place that shows compose's own words.
        toastSuccess({
          title: "Deploying",
          detail: "The agent converges the stack to this file. Its output is on the Logs tab.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not deploy the stack", e, { retry: () => deploy.mutate(vars) }),
    },
  });
  const deployState = useMutationActionState(deploy);

  const s = stack.data;
  const serverName = servers.data?.find((v) => v.id === s?.server_id)?.name ?? s?.server_id ?? "—";
  const observed = shortRevision(s?.observed_revision_id);
  const desired = shortRevision(s?.desired_revision_id);
  // The file the API returns is the CURRENT one — what a deploy would ship —
  // which is not necessarily what is running.
  const drifted = Boolean(desired) && Boolean(observed) && observed !== desired;
  const neverDeployed = !s?.desired_revision_id;

  return (
    <div className="max-w-3xl space-y-5">
      <FactCard
        title="Stack"
        actions={
          <ActionButton
            variant="primary"
            size="sm"
            state={deployState}
            busyLabel="Deploying…"
            successLabel="Deploying"
            onClick={() => deploy.mutate({ id: stackId })}
          >
            <Rocket className="h-3.5 w-3.5" aria-hidden /> Deploy
          </ActionButton>
        }
      >
        <Fact label="Server">{serverName}</Fact>
        <Fact label="Route">
          {s?.route.domain ? (
            <a
              href={`${s.route.https ? "https" : "http"}://${s.route.domain}`}
              target="_blank"
              rel="noreferrer"
              className="underline decoration-border-strong underline-offset-2 hover:decoration-text"
            >
              {s.route.domain}
            </a>
          ) : (
            "internal — nothing published through the Proxy"
          )}
        </Fact>
        {s?.route.domain && (
          <Fact label="Answered by">
            {s.route.service}:{s.route.port}
          </Fact>
        )}
        <Fact label="Running revision">{observed || "nothing yet"}</Fact>
        <Fact label="Desired revision">{desired || "none — deploy to set one"}</Fact>
        <Fact label="Created">{s ? relativeTime(s.created_at) : "—"}</Fact>
      </FactCard>

      {/* Compose's own last words on failure. It never contains a variable's
          value, so it can be shown verbatim. */}
      {s?.status === "error" && s.status_detail && (
        <p className="rounded-md bg-status-error/[0.07] px-3.5 py-3 text-[12.5px] leading-relaxed text-danger">
          {s.status_detail}
        </p>
      )}

      {neverDeployed ? (
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          Nothing is running yet. Deploying points desired state at the file below; the agent runs one{" "}
          <code className="mono text-[11.5px] text-text">docker compose up -d --remove-orphans --wait</code> and reports
          back.
        </p>
      ) : drifted ? (
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          The file below has changed since the running revision. Deploying ships it;{" "}
          <Link
            to="/projects/$projectId/compose/$stackId/revisions"
            params={{ projectId, stackId }}
            className="font-medium underline"
          >
            the revision list
          </Link>{" "}
          is how you go back.
        </p>
      ) : null}

      <section className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <h2 className="eyebrow">The file a deploy would ship</h2>
          <Link
            to="/projects/$projectId/compose/$stackId/settings"
            params={{ projectId, stackId }}
            className="text-[12px] font-medium text-text-mid hover:underline"
          >
            Edit →
          </Link>
        </div>
        <PageState query={file} skeletonRows={6}>
          {(rev) => (
            // Machine text gets the pane treatment every machine value gets:
            // ink ground, mono, legible wherever it is read.
            <pre className="overflow-x-auto rounded-lg border border-pane-border bg-pane px-4 py-3.5 font-mono text-[12px] leading-[1.65] text-pane-text">
              {rev.compose_yaml}
            </pre>
          )}
        </PageState>
      </section>
    </div>
  );
}
