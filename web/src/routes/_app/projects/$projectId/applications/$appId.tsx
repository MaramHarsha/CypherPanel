// Application detail layout: masthead (name, status, the deploy action) plus
// the resource tabs. "Deploy now" lives here rather than on the Deployments
// tab because it is the app's primary action from every tab — you should never
// have to navigate to start a deploy.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { useGetApplication } from "@/api/gen/applications/applications";
import { useGetMe } from "@/api/gen/auth/auth";
import {
  getListDeploymentsQueryKey,
  useDeployApplication,
  useListDeployments,
} from "@/api/gen/deployments/deployments";
import { useGetProject, useListEnvironments } from "@/api/gen/projects/projects";
import { PageBody, PageHeader } from "@/components/page-header";
import { HeaderDomain } from "@/components/domain-link";
import { RedeployPending } from "@/components/redeploy-pending";
import { ResourceGone } from "@/components/resource-gone";
import { StatusBadge } from "@/components/status-badge";
import { ActionButton, useAction } from "@/components/ui/action-button";
import { Dropdown, DropdownContent, DropdownItem, DropdownTrigger } from "@/components/ui/dropdown";
import { useCrumbs } from "@/lib/crumbs";
import { APP_TABS, useDeployShortcut } from "@/lib/keys";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId")({
  component: ApplicationLayout,
});

// The strip is drawn from lib/keys.ts, so the tabs `1`–`7` address (canvas
// 14f) and the tabs on screen cannot drift apart. Storage (canvas 13g) sits
// before Settings as the sitemap orders it; it has no key yet — giving it one
// means adding it to APP_TABS and to the shell's goTab.
//
// `short` is the 360px label (canvas 14c). Only the first NARROW tabs survive
// on a phone; the rest move into "More" rather than off the edge of a strip
// nobody can tell is scrollable.
const KEYED = APP_TABS.map((t) => ({ to: t.segment, label: t.label, short: t.short }));
const STORAGE = { to: "storage", label: "Storage", short: "Storage" } as const;
const TABS = [...KEYED.filter((t) => t.to !== "settings"), STORAGE, ...KEYED.filter((t) => t.to === "settings")];

const NARROW = 4;

/** A deployment is still moving until the plane records a terminal outcome. */
export function isTerminal(status: string): boolean {
  return status === "succeeded" || status === "failed";
}

/**
 * A deploy is LIVE while the plane is working on it. `awaiting_approval` is
 * neither live nor terminal — it is parked, waiting for a person
 * (deploy-protection.md §3) — so it must not hold the Deploy button down.
 */
export function isLive(status: string): boolean {
  return !isTerminal(status) && status !== "awaiting_approval";
}

function ApplicationLayout() {
  const { projectId, appId } = Route.useParams();
  const project = useGetProject(projectId);
  const envs = useListEnvironments(projectId);
  const app = useGetApplication(appId);

  // The trail is team / project / environment / resource (ui-principles §4) —
  // MERIDIAN-STUDIO / ATLAS-CRM / PRODUCTION / WEB, with no "projects" root:
  // the team is the root everywhere in the panel. It is the team that owns the
  // project rather than whatever the sidebar is scoped to, so the trail reads
  // the same for someone looking across every team they belong to; until the
  // membership list has arrived there is no name to use, and a guess would be
  // worse than a shorter trail. The environment crumb is text rather than a
  // link: it addresses a search param on the project home, and a Crumb carries
  // only a pathname.
  const teams = useGetMe().data?.teams ?? [];
  const teamName = teams.find((t) => t.id === project.data?.project.team_id)?.name;
  const envName = (envs.data ?? []).find((e) => e.id === app.data?.environment_id)?.name;
  useCrumbs([
    ...(teamName ? [{ label: teamName, to: "/projects" }] : []),
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    ...(envName ? [{ label: envName }] : []),
    { label: app.data?.name ?? appId },
  ]);

  // Which tab is open, computed rather than left to the strip: below `sm` the
  // folded tabs are display:none, so their own `activeProps` underline is
  // painted on nothing and the strip marks no tab at all. Canvas 14c always
  // marks exactly one, so when the open tab has been folded away the trigger
  // wears its name instead of the word "More".
  const leaf = useRouterState({ select: (s) => s.location.pathname.split("/").pop() ?? "" });
  const activeIndex = TABS.findIndex((t) => t.to === leaf);
  const foldedLabel = activeIndex >= NARROW ? TABS[activeIndex]?.short : undefined;

  // Without this the masthead renders "…" forever over a live tab strip, and
  // the real error hides inside whichever tab is open (ui-principles §1).
  if (app.isError) {
    return (
      <ResourceGone
        kind="application"
        error={app.error}
        backTo={`/projects/${projectId}`}
        backLabel="Back to the project"
      />
    );
  }

  const domain = app.data?.route.domain;
  const https = app.data?.route.https ?? true;
  const status = app.data?.status;

  return (
    <>
      <PageHeader
        size="sm"
        title={app.data?.name ?? "…"}
        badge={
          <span className="flex items-center gap-2.5">
            <StatusBadge status={status} />
            {/* Beside the status, never instead of it: the status vocabulary is
                closed and this is not an observed state (shared-variables.md
                §8). "Deploy now" is already the masthead's primary action, so
                the badge needs no affordance of its own. */}
            {app.data?.redeploy_pending && <RedeployPending />}
            {/* The canvas title row is name · dot · state word and nothing
                else, so the one-click route to the running app stays but drops
                out of the accent: orange is the Deploy pill's here. */}
            {/* The one-click route to the running app — but only when the
                panel will actually serve that hostname. An unverified domain
                links nowhere, and offering the link anyway is how someone ends
                up debugging DNS that was never published. */}
            {domain && <HeaderDomain applicationId={appId} domain={domain} https={https} />}
          </span>
        }
        actions={<DeployButton appId={appId} branch={app.data?.source.branch} />}
        below={
          <nav
            className="-mb-px flex items-center gap-4 text-[12.5px] sm:gap-5 sm:overflow-x-auto sm:text-[13px]"
            aria-label="Application"
          >
            {TABS.map((t, i) => (
              <Link
                key={t.label}
                from={Route.fullPath}
                to={t.to === "" ? "." : t.to}
                activeOptions={{ exact: t.to === "" }}
                className={cn(
                  "whitespace-nowrap border-b-2 border-transparent px-0.5 py-[9px] text-text-mid hover:text-text",
                  i >= NARROW && "hidden sm:block",
                )}
                activeProps={{ className: cn("border-border-strong font-semibold text-text") }}
              >
                {t.short === t.label ? (
                  t.label
                ) : (
                  <>
                    {/* Only the displayed one is in the accessibility tree —
                        the other is display:none, not merely invisible. */}
                    <span className="sm:hidden">{t.short}</span>
                    <span className="hidden sm:inline">{t.label}</span>
                  </>
                )}
              </Link>
            ))}
            <Dropdown>
              <DropdownTrigger
                className={cn(
                  "inline-flex items-center gap-1 whitespace-nowrap border-b-2 border-transparent px-0.5 py-[9px] text-text-mid hover:text-text sm:hidden",
                  foldedLabel && "border-border-strong font-semibold text-text",
                )}
              >
                {foldedLabel ?? "More"} <ChevronDown className="h-3 w-3" aria-hidden />
              </DropdownTrigger>
              <DropdownContent align="end">
                {TABS.slice(NARROW).map((t) => (
                  <DropdownItem key={t.label} asChild>
                    {/* The open tab is marked in the menu too. Weight and a
                        fill rather than a colour: Radix passes the item's own
                        classes through the Slot, where `cn` cannot arbitrate a
                        second text colour against the first. */}
                    <Link
                      from={Route.fullPath}
                      to={t.to === "" ? "." : t.to}
                      activeProps={{ className: "bg-raised font-semibold" }}
                    >
                      {t.label}
                    </Link>
                  </DropdownItem>
                ))}
              </DropdownContent>
            </Dropdown>
          </nav>
        }
      />
      {/* The layout owns the page gutters so every tab is inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}

/** Busy while a deploy is in flight — the plane, not the button, is the source
 *  of truth for whether one is running (ADR-005), so an operator who opens the
 *  app mid-deploy sees the same spinner as the one who started it. */
function DeployButton({ appId, branch }: { appId: string; branch: string | undefined }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { projectId } = Route.useParams();
  const deployments = useListDeployments(appId);
  const active = (deployments.data ?? []).some((d) => isLive(d.status));

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) => {
        toastSuccess("Deploy started");
        // Nothing else refreshes the list until the plane emits an event, and
        // the Deployments tab we are about to land on is drawn from it.
        void qc.invalidateQueries({ queryKey: getListDeploymentsQueryKey(appId) });
        void navigate({
          to: "/projects/$projectId/applications/$appId/deployments",
          params: { projectId, appId },
          search: { dep: d.id },
        });
      },
      onError: (e: unknown, vars) => toastFailed("Deploy failed to start", e, { retry: () => deploy.mutate(vars) }),
    },
  });

  const { state, trigger } = useAction(() => deploy.mutateAsync({ id: appId, data: {} }));

  // `d` (canvas 14f) goes through the same guard as the click: a busy pill
  // swallows both, so a deploy already running is never doubled from the
  // keyboard.
  useDeployShortcut(() => {
    if (active || state === "busy") return;
    void trigger();
  });

  return (
    <ActionButton
      variant="primary"
      // A running deploy outranks the local state machine: the pill stays busy
      // until the plane says the rollout is over, not until the POST returns.
      state={active ? "busy" : state}
      busyLabel="Deploying…"
      successLabel="Started"
      failedLabel="Retry"
      onClick={() => void trigger()}
      title={active ? "A deploy is already running" : branch ? `Deploys ${branch}` : undefined}
    >
      Deploy now
    </ActionButton>
  );
}
