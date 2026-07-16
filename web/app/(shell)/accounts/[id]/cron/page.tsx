"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft, Plus, Save } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/page-header";
import { ApiError, getCron, setCron } from "@/lib/api";
import { useAccount } from "../../use-account";

// Plain-language schedules that expand to real cron fields — the single
// most consistently validated pattern across competitor panels (HestiaCP,
// CyberPanel, and VestaCP each independently built some version of this)
// for making cron approachable without hiding the raw syntax from anyone
// who wants it.
const schedulePresets = [
  { label: "Every 15 minutes", cron: "*/15 * * * *" },
  { label: "Every 30 minutes", cron: "*/30 * * * *" },
  { label: "Every hour", cron: "0 * * * *" },
  { label: "Every day at midnight", cron: "0 0 * * *" },
  { label: "Every day at 2am", cron: "0 2 * * *" },
  { label: "Every Sunday at midnight (weekly)", cron: "0 0 * * 0" },
  { label: "1st of the month at midnight", cron: "0 0 1 * *" },
];

export default function CronPage() {
  const { id } = useParams<{ id: string }>();
  const { account } = useAccount(id);
  const [content, setContent] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [preset, setPreset] = useState(schedulePresets[2].cron);
  const [command, setCommand] = useState("");

  const { data, isFetching } = useQuery({
    queryKey: ["cron", id],
    queryFn: () => getCron(id),
  });

  useEffect(() => {
    if (data) setContent(data.content);
  }, [data]);

  const save = useMutation({
    mutationFn: () => setCron(id, content),
    onSuccess: () => {
      setError(null);
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to save crontab"),
  });

  function insertJob() {
    if (!command.trim()) return;
    const line = `${preset} ${command.trim()}`;
    setContent((c) => (c && !c.endsWith("\n") ? `${c}\n${line}\n` : `${c}${line}\n`));
    setCommand("");
  }

  return (
    <div>
      <Link
        href={`/accounts/${id}`}
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> {account?.username ?? "Account"}
      </Link>
      <PageHeader
        title="Cron jobs"
        description="Jobs run as the account's Linux user. Pick a schedule below to add a job, or edit the crontab directly underneath."
      />

      <Card>
        <CardContent className="grid gap-2 p-4">
          <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Quick add
          </p>
          <div className="grid grid-cols-[1fr_auto] gap-2">
            <div className="grid gap-1">
              <Label htmlFor="cron-preset" className="text-xs text-muted-foreground">
                Schedule
              </Label>
              <Select value={preset} onValueChange={(v) => setPreset(v ?? preset)}>
                <SelectTrigger id="cron-preset">
                  <SelectValue>
                    {(v: string) => schedulePresets.find((p) => p.cron === v)?.label ?? v}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {schedulePresets.map((p) => (
                    <SelectItem key={p.cron} value={p.cron}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1">
              <Label className="text-xs text-muted-foreground">&nbsp;</Label>
              <Button onClick={insertJob} disabled={!command.trim()}>
                <Plus className="h-4 w-4" /> Add
              </Button>
            </div>
          </div>
          <div className="grid gap-1">
            <Label htmlFor="cron-command" className="text-xs text-muted-foreground">
              Command
            </Label>
            <Input
              id="cron-command"
              placeholder="/usr/bin/php ~/public_html/cron.php"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") insertJob();
              }}
              className="font-mono text-xs"
            />
          </div>
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="grid gap-1 p-4">
          <Label htmlFor="cron-raw" className="text-xs text-muted-foreground">
            Advanced: edit the crontab directly — format{" "}
            <code>min hour day month weekday command</code>
          </Label>
          <Textarea
            id="cron-raw"
            className="h-64 resize-none font-mono text-xs"
            value={isFetching ? "loading…" : content}
            onChange={(e) => setContent(e.target.value)}
            spellCheck={false}
          />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex items-center justify-end gap-3 pt-1">
            {saved && <span className="text-sm text-success">Saved</span>}
            <Button onClick={() => save.mutate()} disabled={save.isPending || isFetching}>
              <Save className="h-4 w-4" /> {save.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
