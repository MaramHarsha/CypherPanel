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
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/** The five terminal states a button can be in; `hover` is CSS, not state. */
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
  /**
   * Why the action is unavailable — "viewers can't deploy", "freeze window
   * until Mon 08:00". Presence disables the button and names the reason in a
   * tooltip: a disabled control that does not say why is a dead end.
   */
  disabledReason?: string;
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
  disabled,
  ...props
}: ActionButtonProps) {
  const isDisabled = disabled || Boolean(disabledReason) || state === "busy";

  // Success and failure carry their own colour, so they override the variant
  // rather than tinting it — a green "Deployed" pill is not a primary action.
  const stateClass =
    state === "success"
      ? "border-[1.5px] border-status-running bg-status-running/10 text-status-running hover:bg-status-running/10"
      : state === "failed"
        ? "border-[1.5px] border-status-error bg-status-error/[0.08] text-danger hover:bg-status-error/[0.08]"
        : undefined;

  const button = (
    <Button
      {...props}
      variant={stateClass ? "ghost" : variant}
      disabled={isDisabled}
      aria-busy={state === "busy" || undefined}
      className={cn(
        "grid place-items-center",
        state === "busy" && "opacity-75",
        // A disabled button still has to be readable enough to be recognised
        // as the thing you cannot press, per `10b`.
        disabledReason && "disabled:bg-raised disabled:text-text-disabled disabled:opacity-100",
        stateClass,
        className,
      )}
    >
      {/* Every variant occupies the one cell; the widest sets the width. */}
      <Label live={state === "idle"}>{children}</Label>
      <Label live={state === "busy"}>
        <Loader2 className="size-3 animate-spin" aria-hidden />
        {busyLabel}
      </Label>
      <Label live={state === "success"}>✓ {successLabel}</Label>
      <Label live={state === "failed"}>✕ {failedLabel}</Label>
    </Button>
  );

  if (!disabledReason) return button;
  return (
    <Tooltip content={disabledReason}>
      {/* A disabled button fires no pointer events, so the trigger has to be a
          wrapper or the reason never shows — which is worse than no tooltip,
          because the button then looks arbitrarily dead. */}
      <span className="inline-flex">{button}</span>
    </Tooltip>
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
