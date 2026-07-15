"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Clock, Save } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ApiError, getCron, setCron, type AccountInfo } from "@/lib/api";

export function CronDialog({ account }: { account: AccountInfo }) {
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const { data, isFetching } = useQuery({
    queryKey: ["cron", account.id],
    queryFn: () => getCron(account.id!),
    enabled: open,
  });

  useEffect(() => {
    if (data) setContent(data.content);
  }, [data]);

  const save = useMutation({
    mutationFn: () => setCron(account.id!, content),
    onSuccess: () => {
      setError(null);
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to save crontab"),
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button variant="ghost" size="icon" aria-label="Cron jobs" onClick={() => setOpen(true)}>
        <Clock className="h-4 w-4" />
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Cron jobs — {account.username}</DialogTitle>
          <DialogDescription>
            The account&apos;s crontab. Jobs run as the account&apos;s Linux user.
            Format: <code>min hour day month weekday command</code>.
          </DialogDescription>
        </DialogHeader>
        <textarea
          className="h-64 w-full resize-none rounded-lg border border-border bg-background p-3 font-mono text-xs outline-none focus:ring-2 focus:ring-ring"
          value={isFetching ? "loading…" : content}
          onChange={(e) => setContent(e.target.value)}
          spellCheck={false}
          placeholder={"# m h dom mon dow command\n0 3 * * * /usr/bin/php ~/public_html/cron.php"}
        />
        {error && <p className="text-sm text-destructive">{error}</p>}
        <DialogFooter>
          {saved && <span className="mr-auto self-center text-sm text-success">Saved</span>}
          <Button onClick={() => save.mutate()} disabled={save.isPending || isFetching}>
            <Save className="h-4 w-4" /> {save.isPending ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
