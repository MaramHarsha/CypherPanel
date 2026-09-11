// Guided onboarding — the thread between the golden path's four steps
// (guided-onboarding.md).
//
// Every step already had a screen and an empty state that names its own next
// action. What was missing was the thread: an operator who has just made their
// owner account lands on an empty Projects page that never mentions that a
// project without a server cannot deploy — so the common first experience is
// four steps of work before the panel names the prerequisite.
//
// It is NEVER A WALL. No modal, no redirect, no blocked navigation: someone who
// knows what they are doing scrolls past it, and someone with a half-configured
// panel from an earlier attempt is not trapped in a flow insisting on a state
// they already have.
//
// And it does not come back. Progress is derived, and the band renders only
// while the last step — a deployment that SUCCEEDED — is incomplete, so a panel
// whose last server is decommissioned during maintenance does not greet its
// owner with a beginner's wizard.
import { Link } from "@tanstack/react-router";
import { Check } from "lucide-react";
import { useGetOnboarding } from "@/api/gen/panel/panel";
import { cn } from "@/lib/utils";

type StepName = "owner" | "server" | "project" | "deploy";

const COPY: Record<StepName, { title: string; hint: string; action: string }> = {
  owner: {
    title: "Create your account",
    hint: "Done — you are signed in.",
    action: "",
  },
  server: {
    title: "Add a server",
    hint: "Any Linux host with Docker. The agent dials out over mTLS, so there are no SSH keys to store and no inbound ports. Or use the machine this panel runs on, in one click.",
    action: "Add a server",
  },
  project: {
    title: "Create a project",
    hint: "A project groups the environments and resources for one product.",
    action: "Create a project",
  },
  deploy: {
    title: "Deploy something",
    hint: "A template is the quickest first deploy — the image is already built, so there is no build to get wrong.",
    action: "Browse templates",
  },
};

export function OnboardingBand() {
  const { data } = useGetOnboarding({ query: { retry: false } });
  if (!data || data.done) return null;

  const steps = data.steps.filter((s) => s.name !== "owner" || !s.complete);
  // The first incomplete step is the only one with a live action: "add a
  // server first" is more useful than a button that opens a dialog which then
  // refuses (guided-onboarding.md §4).
  const nextIndex = steps.findIndex((s) => !s.complete);

  return (
    <section
      aria-label="Set up your panel"
      className="mb-5 overflow-hidden rounded-lg border border-border bg-surface"
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-border-subtle px-4 py-3">
        <span className="mono text-[10.5px] uppercase tracking-[0.12em] text-text-faint">Set up your panel</span>
        <span className="text-[12px] text-text-faint">
          {steps.filter((s) => s.complete).length} of {steps.length} done — this disappears once you have deployed
          something.
        </span>
      </div>

      <ol className="divide-y divide-border-subtle">
        {steps.map((step, i) => {
          const copy = COPY[step.name as StepName];
          if (!copy) return null;
          const isNext = i === nextIndex;
          return (
            <li key={step.name} className="flex flex-wrap items-start gap-3 px-4 py-3">
              <span
                aria-hidden
                className={cn(
                  "mt-0.5 flex size-[18px] shrink-0 items-center justify-center rounded-full border text-[10px]",
                  step.complete
                    ? "border-status-running bg-status-running/10 text-status-running"
                    : isNext
                      ? "border-accent text-accent"
                      : "border-border-input text-text-faint",
                )}
              >
                {step.complete ? <Check className="h-3 w-3" /> : i + 1}
              </span>

              <div className="min-w-0 flex-1">
                <p className={cn("text-[13px] font-semibold", step.complete ? "text-text-mid" : "text-text")}>
                  {copy.title}
                  {step.complete && step.count > 0 && (
                    <span className="mono ml-2 text-[11px] font-normal text-text-faint">{step.count}</span>
                  )}
                </p>
                {!step.complete && (
                  <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">{copy.hint}</p>
                )}
              </div>

              {/* Only the next step is actionable. A later step's button would
                  open a dialog that refuses, which teaches nothing. */}
              {!step.complete && isNext && (
                <div className="shrink-0">
                  {step.name === "server" ? (
                    // To the fleet page rather than lifting its dialog up here:
                    // that is where Join a server AND "use this machine" both
                    // live, and one home for an action beats two.
                    <Link
                      to="/servers"
                      className="inline-block rounded-full bg-accent px-4 py-1.5 text-[12.5px] font-semibold text-accent-fg hover:bg-accent-hover"
                    >
                      {copy.action}
                    </Link>
                  ) : step.name === "deploy" ? (
                    <Link
                      to="/templates"
                      className="inline-block rounded-full bg-accent px-4 py-1.5 text-[12.5px] font-semibold text-accent-fg hover:bg-accent-hover"
                    >
                      {copy.action}
                    </Link>
                  ) : null}
                </div>
              )}
              {!step.complete && !isNext && (
                <span className="mono shrink-0 self-center text-[11px] text-text-faint">
                  {steps[nextIndex]?.name === "server" ? "add a server first" : "next"}
                </span>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
