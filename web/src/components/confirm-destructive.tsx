// ConfirmDestructive (ui-principles §2): states the blast radius; irreversible
// actions require typing the resource name; never the default-focused action.
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

interface ConfirmDestructiveProps {
  trigger: ReactNode;
  title: string;
  /** The blast-radius sentence: say exactly what is destroyed. */
  blastRadius: string;
  /** When set, the user must type this (resource name) to enable the action. */
  confirmName?: string;
  actionLabel: string;
  onConfirm: () => void;
  pending?: boolean;
}

export function ConfirmDestructive({
  trigger,
  title,
  blastRadius,
  confirmName,
  actionLabel,
  onConfirm,
  pending,
}: ConfirmDestructiveProps) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const armed = confirmName ? typed === confirmName : true;

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) setTyped("");
      }}
    >
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent title={title} description={blastRadius}>
        {confirmName && (
          <div className="space-y-1.5">
            <p className="text-xs text-text-mid">
              Type <span className="mono text-text">{confirmName}</span> to confirm.
            </p>
            <Input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label={`Type ${confirmName} to confirm`}
            />
          </div>
        )}
        <div className="mt-4 flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button
            variant="danger"
            disabled={!armed || pending}
            onClick={() => {
              onConfirm();
              setOpen(false);
            }}
          >
            {pending ? "Working…" : actionLabel}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
