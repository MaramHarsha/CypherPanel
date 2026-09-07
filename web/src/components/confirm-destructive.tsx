// ConfirmDestructive — design canvas `13af` (dark twin of 9h): "one destructive
// pattern, blast radius listed".
//
// The canvas is specific about why each part is there, and the previous version
// had flattened most of it away:
//
//   · a 3px red top rule, so the modal is recognisable as consequential before
//     a word is read;
//   · the blast radius as an enumerated list in a tinted box, not a sentence —
//     "3 applications, 1 database and its data, 2 preview environments" is
//     scannable in a way that prose is not, and the one item that cannot be
//     undone is the one you must not skim past;
//   · the typed-name confirm, with the name shown as a chip you can compare
//     against character by character;
//   · "audit-logged with your name", stated before the click rather than
//     discovered afterwards.
import { useId, useState, type ReactNode } from "react";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

interface ConfirmDestructiveProps {
  trigger: ReactNode;
  title: string;
  /**
   * What is destroyed. A single sentence still works, but prefer the list —
   * one entry per class of thing, each naming its own consequence. Survivals
   * ("backups in S3 survive") belong in the entry they qualify.
   */
  blastRadius: string | string[];
  /** Sentence above the list. */
  lead?: string;
  /** When set, the user must type this (resource name) to arm the action. */
  confirmName?: string;
  actionLabel: string;
  onConfirm: () => void;
  pending?: boolean;
  /** The verb in progress while `pending` — "Deleting…", "Removing…" (10b). */
  pendingLabel?: string;
}

export function ConfirmDestructive({
  trigger,
  title,
  blastRadius,
  lead = "This permanently removes:",
  confirmName,
  actionLabel,
  onConfirm,
  pending,
  pendingLabel = "Working…",
}: ConfirmDestructiveProps) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const inputId = useId();
  const armed = confirmName ? typed === confirmName : true;
  const items = Array.isArray(blastRadius) ? blastRadius : [blastRadius];

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) setTyped("");
      }}
    >
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent
        size="alert"
        // The canvas gives confirms no ✕: a destructive decision is
        // dismissed by the explicit Cancel, not by a quiet corner glyph.
        hideClose
        title={title}
        description={lead}
        className="border-t-[3px] border-t-status-error"
      >
        {/* A form, so the typed name submits on Enter. Typing a resource name
            and pressing Enter is the natural end of this gesture; without it
            the operator has to leave the keyboard to finish a confirmation
            they have already committed to in writing. The disabled button
            still gates it — an unarmed form submits nothing. */}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!armed || pending) return;
            onConfirm();
            setOpen(false);
          }}
        >
        <ul className="mt-1 rounded-lg border border-status-error/30 bg-surface px-[15px] py-3 font-mono text-[12px] leading-[2] text-text-mid">
          {items.map((item) => (
            <li key={item}>
              {/* Square, not a dot: the shape that means "error" everywhere
                  else in the system (token sheet, status dots). */}
              <span className="text-status-error" aria-hidden>
                ■
              </span>{" "}
              {item}
            </li>
          ))}
        </ul>

        {confirmName && (
          <div className="mt-3">
            <label htmlFor={inputId} className="text-[12.5px] text-text-mid">
              Type{" "}
              <span className="mono rounded-[3px] bg-status-error/[0.07] px-1.5 py-px text-[12px] text-text">
                {confirmName}
              </span>{" "}
              to confirm:
            </label>
            {/* No `outline-none` here, unlike ui/input.tsx: that field trades the
                ring for a border that deepens on focus, and this one is already
                drawn at full ink strength, so removing the outline would leave
                the control that arms an irreversible action with no focus
                indicator at all. It takes the global 14g ring instead. */}
            <input
              id={inputId}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              className="mt-2 w-full rounded-md border-[1.5px] border-border-strong bg-surface px-[11px] py-[9px] font-mono text-[13px] text-text"
            />
          </div>
        )}

        <div className="mt-4 flex items-center gap-2.5">
          <span className="mr-auto text-[11.5px] text-text-faint">audit-logged with your name</span>
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          {/* Filled red at .45 opacity until armed. This is the one place the
              system's grey disabled pill is overridden: 13af keeps the button
              red so the shape of the decision stays visible while it is still
              refused, rather than swapping in a different-looking object. The
              overrides are marked important because Button's grey fill is
              written with a `:not([aria-busy])` selector that outweighs a
              plain `disabled:` variant. An ActionButton, so the pill holds
              its width when the label becomes the verb in progress. */}
          <ActionButton
            type="submit"
            variant="primary"
            state={pending ? "busy" : "idle"}
            busyLabel={pendingLabel}
            disabled={!armed}
            className={cn(
              "bg-status-error text-white hover:bg-danger-hover aria-busy:hover:bg-status-error",
              "disabled:bg-status-error! disabled:text-white! disabled:opacity-45",
            )}
          >
            {actionLabel}
          </ActionButton>
        </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
