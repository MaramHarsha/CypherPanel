"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Settings2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  ApiError,
  changePHPVersion,
  phpIniKeys,
  phpVersions,
  updatePHPSettings,
  type AccountInfo,
} from "@/lib/api";

// Per-key placeholder hints for the allowlisted directives.
const hints: Record<string, string> = {
  memory_limit: "e.g. 512M",
  upload_max_filesize: "e.g. 64M",
  post_max_size: "e.g. 64M",
  max_execution_time: "e.g. 30",
  max_input_time: "e.g. 60",
  max_input_vars: "e.g. 5000",
  display_errors: "On / Off",
};

export function PHPSettingsDialog({ account }: { account: AccountInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [version, setVersion] = useState<string>("");
  const [error, setError] = useState<string | null>(null);

  const { data: keys } = useQuery({
    queryKey: ["php-ini-keys"],
    queryFn: phpIniKeys,
    enabled: open,
  });
  const { data: versions } = useQuery({
    queryKey: ["php-versions"],
    queryFn: phpVersions,
    enabled: open,
  });

  // Seed the form from the account's current settings when opened.
  useEffect(() => {
    if (open) {
      setValues({ ...(account.php_settings ?? {}) } as Record<string, string>);
      setVersion(account.php_version ?? "");
      setError(null);
    }
  }, [open, account.php_settings, account.php_version]);

  const save = useMutation({
    mutationFn: async () => {
      // Version change first (re-provisions the pool onto the new branch),
      // then INI overrides. Each is a no-op when unchanged.
      if (version && version !== account.php_version) {
        await changePHPVersion(account.id!, version);
      }
      const cleaned = Object.fromEntries(
        Object.entries(values).filter(([, v]) => v.trim() !== ""),
      );
      const before = JSON.stringify(account.php_settings ?? {});
      if (JSON.stringify(cleaned) !== before) {
        await updatePHPSettings(account.id!, cleaned);
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
      setOpen(false);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to update settings"),
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="icon"
        aria-label="PHP settings"
        onClick={() => setOpen(true)}
      >
        <Settings2 className="h-4 w-4" />
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>PHP settings — {account.username}</DialogTitle>
          <DialogDescription>
            Choose the PHP version and override directives. Overrides apply as
            pool-level php_admin_value; blank uses the server default.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 items-center gap-3">
            <Label htmlFor="php-version" className="text-xs font-mono">
              php_version
            </Label>
            <Select value={version} onValueChange={(v) => setVersion(v ?? "")}>
              <SelectTrigger id="php-version" className="col-span-2">
                <SelectValue placeholder="Select version" />
              </SelectTrigger>
              <SelectContent>
                {(versions ?? []).map((v) => (
                  <SelectItem key={v} value={v}>
                    PHP {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="h-px bg-border" />
          {(keys ?? []).map((k) => (
            <div key={k} className="grid grid-cols-3 items-center gap-3">
              <Label htmlFor={`php-${k}`} className="text-xs font-mono">
                {k}
              </Label>
              <Input
                id={`php-${k}`}
                className="col-span-2"
                placeholder={hints[k] ?? ""}
                value={values[k] ?? ""}
                onChange={(e) => setValues((s) => ({ ...s, [k]: e.target.value }))}
              />
            </div>
          ))}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button onClick={() => save.mutate()} disabled={save.isPending}>
            {save.isPending ? "Applying…" : "Save & apply"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
