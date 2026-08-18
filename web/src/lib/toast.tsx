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
// Rendered with toast.custom because the panel's toast is ink in both themes
// with a status dot whose *shape* carries meaning (round = ok, square = error,
// per the colour-blind rule in the token sheet). Sonner's built-in variants
// cannot express that.
import { toast as sonner } from "sonner";
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";

type ToastId = string | number;

interface ToastBody {
  title: ReactNode;
  /** The second line: consequence, cause, or what happens next. */
  detail?: ReactNode;
  /** Inline actions — "Retry", "Deploy now". Rendered in the toast's own ink. */
  actions?: { label: string; onClick: () => void }[];
}

function Shell({
  mark,
  title,
  detail,
  actions,
  onDismiss,
}: ToastBody & { mark: ReactNode; onDismiss?: () => void }) {
  return (
    <div className="flex w-[340px] items-start gap-[11px] rounded-[10px] bg-toast px-4 py-[13px] text-toast-text shadow-[0_12px_32px_rgba(0,0,0,.25)]">
      {mark}
      <div className="flex-1">
        <div className="text-[13px] font-semibold">{title}</div>
        {(detail || actions?.length) && (
          <div className="mt-0.5 text-[12px] leading-[1.5] text-toast-faint">
            {detail}
            {detail && actions?.length ? <br /> : null}
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

export function toastSuccess(body: ToastBody, id?: ToastId) {
  return sonner.custom(
    (t) => <Shell {...body} mark={<Dot className="bg-[#4ac26b]" />} onDismiss={() => sonner.dismiss(t)} />,
    { id, duration: 5000, unstyled: true },
  );
}

/**
 * Errors persist. `actions` is not optional in spirit — an error toast with no
 * next step is a dead end, and `10c` never shows one.
 */
export function toastError(body: ToastBody, id?: ToastId) {
  return sonner.custom(
    (t) => (
      <Shell {...body} mark={<Dot square className="bg-[#ff6a5e]" />} onDismiss={() => sonner.dismiss(t)} />
    ),
    { id, duration: Infinity, unstyled: true },
  );
}

/**
 * A job in flight. Returns its id; pass that id back to toastSuccess or
 * toastError so the same toast morphs rather than a second one appearing.
 * No dismiss affordance: dismissing would suggest the work stopped.
 */
export function toastWorking(body: ToastBody, id?: ToastId) {
  return sonner.custom(
    () => (
      <Shell
        {...body}
        mark={
          <span
            aria-hidden
            className="mt-[3px] size-3 flex-none animate-spin rounded-full border-2 border-pane-border border-t-[#58a6ff]"
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
    working: ToastBody;
    success: (value: T) => ToastBody;
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
