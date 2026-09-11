// The "?" overlay — canvas 14f. A 480px paper card on the ink scrim listing
// the eight shortcuts in the canvas's own order, two columns, each row a kbd
// cap and a plain-language label, and the sentence that makes the whole
// vocabulary safe to learn by pressing things: nothing here is destructive.
//
// The list is the contract: a key printed here must work, and a key that
// works must be printed here. The shell (routes/_app.tsx) and the row hook
// (lib/keys.ts) are the two places that honour it.
import { Dialog, DialogContent } from "@/components/ui/dialog";

export const SHORTCUTS: readonly (readonly [string, string])[] = [
  ["⌘K", "jump to anything"],
  ["g p", "go to projects"],
  ["g s", "go to servers"],
  ["g i", "inbox"],
  ["d", "deploy (on an app)"],
  ["l", "logs (on an app)"],
  ["j / k", "next / previous row"],
  ["1–7", "app tabs"],
];

export function ShortcutsOverlay({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* 14f draws its own head — the title and the "esc closes" hint share one
          baseline, and there is no ✕ — so the card's masthead is carried for
          the accessible name only. Zeroing the padding it keeps while hidden
          is what lets this head sit where the canvas puts it. */}
      <DialogContent
        title="Keyboard shortcuts"
        description="Press a key anywhere outside a text field. Escape closes this list."
        hideTitle
        hideClose
        className="max-w-[480px] [&>div]:p-0"
      >
        <div className="px-[26px] py-[22px]">
          <div className="mb-4 flex items-baseline">
            <span className="text-[17px] font-bold tracking-[-0.02em] text-text">Keyboard shortcuts</span>
            <span className="ml-auto font-mono text-[11px] text-text-faint">? anywhere · esc closes</span>
          </div>
          <dl className="grid grid-cols-1 gap-x-7 gap-y-1.5 sm:grid-cols-2">
            {SHORTCUTS.map(([keys, what]) => (
              <div key={keys} className="flex items-center gap-2.5 py-1.5">
                <dt>
                  <kbd className="rounded border border-border-input bg-surface px-[7px] py-0.5 font-mono text-[11px] font-normal text-text">
                    {keys}
                  </kbd>
                </dt>
                <dd className="text-[13px] text-text-dim">{what}</dd>
              </div>
            ))}
          </dl>
          <p className="mt-3.5 text-[12px] text-text-faint">
            No shortcut triggers anything destructive — deletes and rollbacks always go through their
            confirms.
          </p>
        </div>
      </DialogContent>
    </Dialog>
  );
}
