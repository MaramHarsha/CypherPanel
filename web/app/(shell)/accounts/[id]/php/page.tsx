"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
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
import { PageHeader } from "@/components/page-header";
import { cn } from "@/lib/utils";
import { ApiError, changePHPVersion, phpIniKeys, phpVersions, updatePHPSettings } from "@/lib/api";
import { useAccount } from "../../use-account";

// Plain-language metadata for the allowlisted php.ini directives (internal/phpini
// owns the actual allowlist). Anything the backend allows that isn't listed here
// still renders — just with its raw key as the label — so this page can never
// go stale-blocking when the allowlist grows.
const fieldMeta: Record<
  string,
  { label: string; desc: string; placeholder?: string; triState?: boolean }
> = {
  memory_limit: {
    label: "Memory limit",
    desc: "Max memory a single script can use.",
    placeholder: "e.g. 512M",
  },
  upload_max_filesize: {
    label: "Max upload size",
    desc: "Largest file a script can accept via upload.",
    placeholder: "e.g. 64M",
  },
  post_max_size: {
    label: "Max request size",
    desc: "Largest total request body, including uploads — keep this ≥ upload size.",
    placeholder: "e.g. 64M",
  },
  max_execution_time: {
    label: "Max execution time",
    desc: "Seconds a script may run before PHP stops it.",
    placeholder: "e.g. 30",
  },
  max_input_time: {
    label: "Max input parsing time",
    desc: "Seconds allowed to parse incoming request data.",
    placeholder: "e.g. 60",
  },
  max_input_vars: {
    label: "Max input variables",
    desc: "Max form fields / array entries parsed per request.",
    placeholder: "e.g. 5000",
  },
  display_errors: {
    label: "Show PHP errors on-page",
    desc: "Debugging aid only — leave on Server default (off) in production to avoid leaking file paths.",
    triState: true,
  },
};

// A three-way toggle (not a plain on/off switch) because every override here
// can also be *unset* to inherit the server's default — losing that middle
// state would be a regression from the previous "blank = default" behavior.
function TriStateField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const options: { v: string; label: string }[] = [
    { v: "", label: "Default" },
    { v: "On", label: "On" },
    { v: "Off", label: "Off" },
  ];
  return (
    <div className="inline-flex shrink-0 rounded-lg border border-input bg-muted/40 p-0.5">
      {options.map((o) => (
        <button
          key={o.v}
          type="button"
          onClick={() => onChange(o.v)}
          className={cn(
            "rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
            value === o.v
              ? "bg-primary text-primary-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export default function PHPSettingsPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account } = useAccount(id);
  // Drafts stay null until the operator edits; the form renders the account's
  // current settings until then. Deriving beats copying the fetched account
  // into state via an effect, which would cascade a render on every refetch
  // and could clobber unsaved edits.
  const [draftValues, setDraftValues] = useState<Record<string, string> | null>(null);
  const [draftVersion, setDraftVersion] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const { data: keys } = useQuery({ queryKey: ["php-ini-keys"], queryFn: phpIniKeys });
  const { data: versions } = useQuery({ queryKey: ["php-versions"], queryFn: phpVersions });

  const values = draftValues ?? ((account?.php_settings ?? {}) as Record<string, string>);
  const version = draftVersion ?? account?.php_version ?? "";

  // Edits start from whatever is currently displayed, so the first keystroke
  // seeds the draft from server state rather than from an empty object.
  const setValues = (update: (prev: Record<string, string>) => Record<string, string>) =>
    setDraftValues((prev) => update(prev ?? ((account?.php_settings ?? {}) as Record<string, string>)));

  const save = useMutation({
    mutationFn: async () => {
      // Version change first (re-provisions the pool onto the new branch),
      // then INI overrides. Each is a no-op when unchanged.
      if (version && version !== account?.php_version) {
        await changePHPVersion(id, version);
      }
      const cleaned = Object.fromEntries(
        Object.entries(values).filter(([, v]) => v.trim() !== ""),
      );
      const before = JSON.stringify(account?.php_settings ?? {});
      if (JSON.stringify(cleaned) !== before) {
        await updatePHPSettings(id, cleaned);
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
      setError(null);
      setSaved(true);
      // Hand the form back to server state so it shows what was actually
      // stored (blank overrides are dropped server-side), not what was typed.
      setDraftValues(null);
      setDraftVersion(null);
      setTimeout(() => setSaved(false), 2000);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to update settings"),
  });

  return (
    <div>
      <Link
        href={`/accounts/${id}`}
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> {account?.username ?? "Account"}
      </Link>
      <PageHeader
        title="PHP settings"
        description="Choose the PHP version and override its defaults for this account only. Leave a field blank to keep using the server default."
      />

      <Card>
        <CardContent className="grid gap-5 p-5">
          <div className="grid gap-1.5">
            <Label htmlFor="php-version">PHP version</Label>
            <Select value={version} onValueChange={(v) => setDraftVersion(v ?? "")}>
              <SelectTrigger id="php-version" className="sm:w-64">
                <SelectValue>{(v: string | null) => (v ? `PHP ${v}` : "Select version")}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                {(versions ?? []).map((v) => (
                  <SelectItem key={v} value={v}>
                    PHP {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Changing this re-provisions the account&apos;s PHP-FPM pool on the new version.
            </p>
          </div>

          <div className="h-px bg-border" />

          <div className="grid gap-4">
            <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Performance &amp; limits
            </p>
            {(keys ?? []).map((k) => {
              const meta = fieldMeta[k] ?? { label: k, desc: "" };
              return (
                <div key={k} className="grid gap-1">
                  <div className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <Label htmlFor={`php-${k}`} className="text-sm font-medium">
                        {meta.label}
                      </Label>
                      <p className="font-mono text-[10px] text-muted-foreground/60">{k}</p>
                    </div>
                    {meta.triState ? (
                      <TriStateField
                        value={values[k] ?? ""}
                        onChange={(v) => setValues((s) => ({ ...s, [k]: v }))}
                      />
                    ) : (
                      <Input
                        id={`php-${k}`}
                        className="w-32 shrink-0 text-right"
                        placeholder={meta.placeholder}
                        value={values[k] ?? ""}
                        onChange={(e) => setValues((s) => ({ ...s, [k]: e.target.value }))}
                      />
                    )}
                  </div>
                  {meta.desc && <p className="text-xs text-muted-foreground">{meta.desc}</p>}
                </div>
              );
            })}
          </div>

          {error && <p className="text-sm text-destructive">{error}</p>}
          <div className="flex items-center justify-end gap-3">
            {saved && <span className="text-sm text-success">Saved &amp; applied</span>}
            <Button onClick={() => save.mutate()} disabled={save.isPending}>
              {save.isPending ? "Applying…" : "Save & apply"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
