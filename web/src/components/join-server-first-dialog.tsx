// Golden path (ui-principles §11): with no server joined, "+ New application"
// and "+ New database" cannot go anywhere useful — so instead of a form that
// would create a resource nothing can run, or a silent jump to another page,
// the trigger opens this and says why the next step is over there.
import { useNavigate } from "@tanstack/react-router";
import { type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogTrigger } from "@/components/ui/dialog";

export function JoinServerFirstDialog({
  trigger,
  resource,
}: {
  /** The same pill the real create dialog would hang off. */
  trigger: ReactNode;
  resource: "application" | "database";
}) {
  const navigate = useNavigate();
  const noun = resource === "application" ? "An application" : "A database";
  return (
    <Dialog>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent
        title="Join a server first"
        description={`${noun} runs on one of your servers, and none has joined yet. Joining takes one copy-paste command and about a minute.`}
      >
        <div className="flex justify-end">
          <Button variant="primary" onClick={() => void navigate({ to: "/servers" })}>
            Go to Servers
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
