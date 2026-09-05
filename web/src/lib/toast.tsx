// Toasts — design canvas `10c`. Three behaviours, and the difference between
// them is the whole point:
//
//   success  5s, then gone.
//   error    stays until dismissed, and always carries a next step.
//   working  lives until the job resolves, then morphs in place.
//
// "Morphs in place" is why these wrap sonner's id-reuse rather than firing a
// second toast: a working toast that resolves into a *new* toast reads as two
// events, and the operator has to work out that they were one.
//
// This module is the panel's ONLY toast surface. Nothing else imports sonner
// (the Toaster in routes/__root.tsx aside): a raw `toast.error(e.message)`
// auto-dismisses in five seconds and carries no next step, which is exactly
// the toast `10c` forbids — and once one exists it gets copied.
//
// Rendered with toast.custom because the panel's toast is ink in both themes
// with a status dot whose *shape* carries meaning (round = ok, square = error,
// per the colour-blind rule in the token sheet). Sonner's built-in variants
// cannot express that.
import { toast as sonner } from "sonner";
import { type ReactNode, useState } from "react";
import { ApiError, NetworkError, faultBundleOf } from "@/api/client";
import { cn } from "@/lib/utils";

export type ToastId = string | number;

export interface ToastAction {
  label: string;
  onClick: () => void;
}

export interface ToastBody {
  title: ReactNode;
  /** The second line: consequence, cause, or what happens next. */
  detail?: ReactNode;
  /** Inline actions — "Retry", "Deploy now". Rendered in the toast's own ink. */
  actions?: ToastAction[];
  /**
   * Raw material behind a `details ▾` expander — the route, the status, the
   * server's own words. ui-principles §1: the headline is in glossary terms,
   * and the technical answer sits one click behind it rather than in it.
   */
  details?: string;
}

/** `toastSuccess("Saved")` and `toastSuccess({ title: "Saved", … })` are both fine. */
type Copy = ToastBody | string;
const body = (c: Copy): ToastBody => (typeof c === "string" ? { title: c } : c);

function Details({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="text-toast-faint underline-offset-2 hover:text-toast-text hover:underline"
      >
        details {open ? "▴" : "▾"}
      </button>
      {open && (
        <pre className="mt-1.5 max-h-32 overflow-auto whitespace-pre-wrap break-all rounded bg-white/5 px-2 py-1.5 font-mono text-[11px] leading-[1.5] text-toast-faint">
          {text}
        </pre>
      )}
    </>
  );
}

function Shell({
  mark,
  title,
  detail,
  actions,
  details,
  onDismiss,
}: ToastBody & { mark: ReactNode; onDismiss?: () => void }) {
  const hasActions = Boolean(actions?.length) || Boolean(details);
  return (
    // 360px ink card, 10px radius (`10c`); on a phone it yields to the gutters.
    <div className="flex w-[360px] max-w-[calc(100vw-2rem)] items-start gap-[11px] rounded-[10px] bg-toast px-4 py-[13px] text-toast-text shadow-[0_12px_32px_rgba(0,0,0,.25)]">
      {mark}
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-semibold">{title}</div>
        {(detail || hasActions) && (
          <div className="mt-0.5 text-[12px] leading-[1.5] text-toast-faint">
            {detail}
            {detail && hasActions ? <br /> : null}
            {actions?.map((a, i) => (
              <span key={a.label}>
                {i > 0 && " · "}
                <button
                  type="button"
                  onClick={a.onClick}
                  className="font-semibold text-toast-text underline-offset-2 hover:underline"
                >
                  {a.label}
                </button>
              </span>
            ))}
            {details && (
              <>
                {actions?.length ? " · " : null}
                <Details text={details} />
              </>
            )}
          </div>
        )}
      </div>
      {onDismiss && (
        <button
          type="button"
          onClick={onDismiss}
          aria-label="Dismiss"
          className="text-[12px] leading-none text-toast-dismiss hover:text-toast-text"
        >
          ✕
        </button>
      )}
    </div>
  );
}

/** Round dot: ok / neutral. Square: error. Shape as well as colour. */
function Dot({ className, square }: { className: string; square?: boolean }) {
  return (
    <span
      aria-hidden
      className={cn("mt-[5px] size-2 flex-none", square ? "rounded-[2px]" : "rounded-full", className)}
    />
  );
}

export function toastSuccess(copy: Copy, id?: ToastId) {
  return sonner.custom(
    (t) => <Shell {...body(copy)} mark={<Dot className="bg-pane-ok" />} onDismiss={() => sonner.dismiss(t)} />,
    { id, duration: 5000, unstyled: true },
  );
}

/**
 * Errors persist. `actions` is not optional in spirit — an error toast with no
 * next step is a dead end, and `10c` never shows one. Most call sites want
 * `toastFailed` below, which works the next step out from the error itself.
 */
export function toastError(copy: ToastBody, id?: ToastId) {
  return sonner.custom(
    (t) => (
      <Shell {...copy} mark={<Dot square className="bg-pane-error" />} onDismiss={() => sonner.dismiss(t)} />
    ),
    { id, duration: Infinity, unstyled: true },
  );
}

/**
 * A job in flight. Returns its id; pass that id back to toastSuccess or
 * toastError so the same toast morphs rather than a second one appearing.
 * No dismiss affordance: dismissing would suggest the work stopped.
 */
export function toastWorking(copy: Copy, id?: ToastId) {
  return sonner.custom(
    () => (
      <Shell
        {...body(copy)}
        mark={
          <span
            aria-hidden
            className="mt-[3px] size-3 flex-none animate-spin rounded-full border-2 border-pane-border border-t-pane-info motion-reduce:animate-none"
          />
        }
      />
    ),
    { id, duration: Infinity, unstyled: true },
  );
}

/**
 * The whole arc in one call: working → success | error, in place.
 *
 * Callers reach for this rather than composing the three by hand, because the
 * id has to be threaded through for the morph to happen and forgetting it is
 * silent — you get two toasts and nobody notices in review.
 */
export async function toastThrough<T>(
  run: Promise<T>,
  copy: {
    working: Copy;
    success: (value: T) => Copy;
    error: (err: unknown) => ToastBody;
  },
): Promise<T> {
  const id = toastWorking(copy.working);
  try {
    const value = await run;
    toastSuccess(copy.success(value), id);
    return value;
  } catch (err) {
    toastError(copy.error(err), id);
    throw err;
  }
}

/**
 * Clipboard access exists only in a secure context — https, or localhost — and
 * a self-hosted panel is routinely reached at `http://<ip>:<port>` before TLS
 * is set up, so the old selection-based copy stays as the fallback.
 */
export async function copyText(value: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Denied by permissions policy — fall through to the legacy path.
    }
  }
  const ta = document.createElement("textarea");
  ta.value = value;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}

/** What a failure means for the operator, and which next steps make sense. */
function describeFailure(err: unknown): { detail: string; retryable: boolean; copyable: boolean } {
  if (err instanceof NetworkError) {
    return {
      detail: "Can't reach the control plane — your apps are still serving.",
      retryable: true,
      copyable: false,
    };
  }
  if (err instanceof ApiError) {
    if (err.status >= 500) {
      return {
        detail: "The panel hit a bug — your fleet didn't. Copy the details if you file an issue.",
        retryable: true,
        copyable: true,
      };
    }
    if (err.status === 403) {
      return {
        detail: "Needs a higher role than yours — an owner can grant it.",
        retryable: false,
        copyable: false,
      };
    }
    if (err.status === 404) {
      return {
        detail: "It isn't here any more — a teammate may have deleted it.",
        retryable: false,
        copyable: false,
      };
    }
    // 429/408 will succeed if asked again; a 400/409/422 needs different input,
    // and the server's own sentence is the instruction for what to change.
    return { detail: err.message, retryable: err.status === 429 || err.status === 408, copyable: false };
  }
  // Something the panel itself threw while handling the answer — a bug.
  return {
    detail: err instanceof Error && err.message ? err.message : "Something went wrong inside the panel.",
    retryable: true,
    copyable: true,
  };
}

/**
 * The error toast every mutation's `onError` should reach for. `title` is the
 * glossary-term headline the call site already knows ("Deploy failed to
 * start"); the cause and the next step are worked out from the error:
 *
 *   · no response            → "Retry"
 *   · 5xx                    → "Retry · Copy details" and the route behind `details ▾`
 *   · 403 / 404              → the fix in words (nothing to retry)
 *   · other 4xx              → the server's own sentence
 *
 * `retry` is the same mutation with the same variables. It is only offered
 * where trying again can change the answer.
 */
export function toastFailed(
  title: string,
  err: unknown,
  opts: { retry?: () => void; actions?: ToastAction[]; id?: ToastId } = {},
) {
  const f = describeFailure(err);
  const bundle = faultBundleOf(err);
  const actions: ToastAction[] = [];
  if (opts.retry && f.retryable) actions.push({ label: "Retry", onClick: opts.retry });
  if (opts.actions) actions.push(...opts.actions);
  if (f.copyable && bundle) {
    actions.push({
      label: "Copy details",
      onClick: () => {
        void copyText(bundle).then((ok) =>
          ok ? toastSuccess("Copied") : toastError({ title: "Could not copy", detail: "Open details ▾ and select it." }),
        );
      },
    });
  }
  return toastError({ title, detail: f.detail, actions, details: bundle || undefined }, opts.id);
}
