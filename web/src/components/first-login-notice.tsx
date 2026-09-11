// What a person needs the moment a template finishes installing
// (docs/features/template-catalog.md §4.1).
//
// Before this, an install ended by navigating to an application page — and left
// you at a URL you could not get into. Some templates run on an upstream
// default nobody told you (Grafana is admin/admin), some make you create the
// account yourself, and twenty-four of them had a password the panel GENERATED
// and then sealed away where no one could ever read it. All three needed
// saying, and the last one needs saying exactly once.
import { useState } from "react";
import { CopyButton } from "@/components/copy-field";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import type { FirstLogin } from "@/api/gen/model";

export function FirstLoginNotice({
  first,
  templateName,
  onContinue,
}: {
  first: FirstLogin;
  templateName: string;
  onContinue: () => void;
}) {
  const [open, setOpen] = useState(true);
  const close = () => {
    setOpen(false);
    onContinue();
  };

  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent size="form" title={`${templateName} is installing`}>
        <div className="space-y-4">
          {first.kind === "credentials" && (
            <>
              <p className="text-[13px] leading-relaxed text-text-dim">
                {first.generated
                  ? "These were generated for this install. This is the only time they are shown — copy them somewhere safe now."
                  : "Sign in with these. They are the image's documented defaults, so treat them as public until you change them."}
              </p>
              <dl className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
                {first.username && <CredRow label="Username" value={first.username} />}
                {first.password && <CredRow label="Password" value={first.password} secret={first.generated} />}
              </dl>
              {first.generated && (
                <p className="flex items-start gap-1.5 text-[11.5px] leading-relaxed text-status-degraded-text">
                  {/* Square mark: this needs a person, now (ui-principles §5). */}
                  <span className="mt-[0.42rem] size-[6px] shrink-0 bg-status-degraded" aria-hidden />
                  <span>
                    Not stored anywhere you can read it back. If you lose it, the only way in is to reset the password
                    from inside the app or reinstall.
                  </span>
                </p>
              )}
            </>
          )}

          {first.kind === "setup" && (
            <p className="text-[13px] leading-relaxed text-text-dim">
              There is no password to hand you — this app creates the account itself.
            </p>
          )}

          {first.kind === "none" && (
            <p className="text-[13px] leading-relaxed text-text-dim">
              Nothing to sign into. Open it and use it.
            </p>
          )}

          {first.note && (
            <p className="rounded-lg border border-border bg-bg px-3.5 py-3 text-[12.5px] leading-relaxed text-text-mid">
              {first.note}
            </p>
          )}

          <p className="text-[11.5px] leading-relaxed text-text-faint">
            The app is deploying now — give it a moment before the domain answers.
          </p>

          <div className="flex justify-end">
            <Button variant="primary" onClick={close}>
              {first.kind === "credentials" && first.generated ? "I have copied it" : "Continue"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function CredRow({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-2.5">
      <dt className="shrink-0 text-[12px] text-text-faint">{label}</dt>
      <dd className="flex min-w-0 items-center gap-2">
        <span className={`mono truncate text-[12.5px] ${secret ? "text-text" : "text-text-mid"}`}>{value}</span>
        <CopyButton value={value} label={`Copy ${label.toLowerCase()}`} />
      </dd>
    </div>
  );
}
