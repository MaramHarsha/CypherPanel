// The action button vocabulary — design canvas `10b`, "every action button has
// 6 states". Ordinary Button covers idle/hover/disabled; this adds the three
// states that only exist once a click is in flight, and the rule that makes
// them usable: the label changes to the verb in progress, and the button never
// changes size while it does.
//
// Width is reserved by rendering every label variant in the same grid cell and
// hiding all but the live one. The alternative — measuring on mount — reflows
// on font load and lies during the first paint, which is exactly the jump this
// exists to prevent.
//
// Two ways to drive it, both in this file:
//   · `useAction(run)` for an action the button owns end to end (the deploy
//     pill): idle → busy → success (held 2s) → idle, or → failed until retried;
//   · `useMutationActionState(mutation)` for a button whose work is a
//     TanStack mutation the page already holds: same machine, derived from
//     `mutation.status`, so `state={m.isPending ? "busy" : "idle"}` — which
//     can never show success or offer a retry — is no longer the easy path.
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** The states a button can be in; `hover` is CSS, not state. */
export type ActionState = "idle" | "busy" | "success" | "failed";

export interface ActionButtonProps extends Omit<ButtonProps, "children"> {
  state?: ActionState;
  /** Idle label — "Deploy now". */
  children: ReactNode;
  /** Busy label — the verb in progress, "Deploying…". */
  busyLabel?: ReactNode;
  /** Success label, held ~2s by useAction before returning to idle. */
  successLabel?: ReactNode;
  /** Failure label; clicking retries. The toast carries the why (`10c`). */
  failedLabel?: ReactNode;
}

export function ActionButton({
  state = "idle",
  children,
  busyLabel = "Working…",
  successLabel = "Done",
  failedLabel = "Failed — retry",
  disabledReason,
  className,
  variant,
  ...props
}: ActionButtonProps) {
  // Success and failure carry their own colour, so they override the variant
  // rather than tinting it — a green "Deployed" pill is not a primary action.
  const stateClass =
    state === "success"
      ? "border-[1.5px] border-status-running bg-status-running/10 font-semibold text-status-running hover:bg-status-running/10 hover:text-status-running hover:shadow-none"
      : state === "failed"
        ? "border-[1.5px] border-status-error bg-status-error/[0.08] font-semibold text-danger hover:bg-status-error/[0.08] hover:text-danger hover:shadow-none"
        : undefined;

  return (
    <>
      <Button
        {...props}
        variant={stateClass ? "ghost" : variant}
        // A busy pill is inert but keeps focus (Button turns aria-disabled
        // into a swallowed click), so the ring stays where the operator
        // pressed and the outcome is read from the same spot.
        aria-disabled={state === "busy" || undefined}
        aria-busy={state === "busy" || undefined}
        // A reason may ride a busy pill too — "a deploy is already running"
        // beside "Deploying…" — since the grey fill is scoped to not-busy.
        disabledReason={disabledReason}
        className={cn("grid place-items-center", state === "busy" && "opacity-75", stateClass, className)}
      >
        {/* Every variant occupies the one cell; the widest sets the width. */}
        <Label live={state === "idle"}>{children}</Label>
        <Label live={state === "busy"}>
          <Spinner />
          {busyLabel}
        </Label>
        <Label live={state === "success"}>✓ {successLabel}</Label>
        <Label live={state === "failed"}>✕ {failedLabel}</Label>
      </Button>
      {/* The label swap is visual; this is what a screen reader hears. Polite,
          so it queues behind whatever is being read rather than cutting in
          (canvas 14g LIVE REGIONS). */}
      <span className="sr-only" aria-live="polite" aria-atomic="true">
        {state === "busy" ? busyLabel : state === "success" ? successLabel : state === "failed" ? failedLabel : null}
      </span>
    </>
  );
}

/**
 * 10b's 12px ring — 2px of the label colour at .35 with a solid lead — so it
 * sits inside the ink pill and inside the inverted paper one without a second
 * colour. Under `prefers-reduced-motion` it becomes the static ▸ canvas 14g
 * names: a frozen ring reads as a fault, a glyph reads as "in progress".
 */
function Spinner() {
  return (
    <>
      <span
        aria-hidden
        className="inline-block size-3 flex-none animate-spin rounded-full border-2 border-current/35 border-t-current motion-reduce:hidden"
      />
      <span aria-hidden className="hidden flex-none leading-none motion-reduce:inline">
        ▸
      </span>
    </>
  );
}

function Label({ live, children }: { live: boolean; children: ReactNode }) {
  return (
    <span
      aria-hidden={!live}
      className={cn(
        "col-start-1 row-start-1 inline-flex items-center gap-2 whitespace-nowrap",
        !live && "invisible",
      )}
    >
      {children}
    </span>
  );
}

/**
 * Drives the state machine `10b` describes: idle → busy → success (held 2s) →
 * idle, or → failed, which stays until the next click retries.
 *
 * Held in a hook rather than left to each caller because the 2s hold is the
 * part everyone forgets, and without it the success state flashes for one
 * frame and reads as a glitch.
 */
export function useAction(run: () => Promise<unknown>, successHoldMs = 2000) {
  const [state, setState] = useState<ActionState>("idle");
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      clearTimeout(timer.current);
    };
  }, []);

  const trigger = useCallback(async () => {
    // Guard on the ref, not on `state`: two clicks inside one render would
    // both read the stale value and fire the action twice.
    if (!alive.current) return;
    setState((s) => (s === "busy" ? s : "busy"));
    try {
      await run();
      if (!alive.current) return;
      setState("success");
      timer.current = setTimeout(() => alive.current && setState("idle"), successHoldMs);
    } catch {
      // The toast carries the why (`10c`); the button only offers the retry.
      if (alive.current) setState("failed");
    }
  }, [run, successHoldMs]);

  return { state, trigger };
}

/**
 * The same machine, read off a TanStack mutation the caller already owns:
 *
 *   const create = useCreateThing({ mutation: { onError: toastError } });
 *   <ActionButton state={useMutationActionState(create)} busyLabel="Creating…">
 *
 * `pending` is busy, `error` is failed (the pill offers the retry, the toast
 * carries the why), and `success` is held 2s before the pill returns to idle
 * even though the mutation itself stays `success` until it is next fired.
 * Accepts any object with a mutation-shaped `status` so it does not pin the
 * generated hooks' generics.
 */
export function useMutationActionState(
  mutation: { status: "idle" | "pending" | "success" | "error" },
  successHoldMs = 2000,
): ActionState {
  const [state, setState] = useState<ActionState>("idle");
  const { status } = mutation;
  useEffect(() => {
    if (status === "pending") {
      setState("busy");
      return;
    }
    if (status === "error") {
      setState("failed");
      return;
    }
    if (status === "success") {
      setState("success");
      const t = setTimeout(() => setState("idle"), successHoldMs);
      return () => clearTimeout(t);
    }
    setState("idle");
  }, [status, successHoldMs]);
  return state;
}
