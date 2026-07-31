"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plug, Trash2, Upload } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/page-header";
import {
  ApiError,
  installPlugin,
  listPlugins,
  setPluginEnabled,
  uninstallPlugin,
  type PluginInfo,
} from "@/lib/api";

const sampleManifest = `api_version: v1
name: my-plugin
version: 1.0.0
kind: plugin
description: What this plugin does
author: You
ui:
  sidebar:
    - label: My Plugin
      path: /my-plugin
      icon: box
events:
  - events.account.created
permissions:
  - accounts:read
`;

function UninstallButton({ plugin }: { plugin: PluginInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => uninstallPlugin(plugin.name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["plugins"] });
      setOpen(false);
    },
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Uninstall ${plugin.name}`}
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-4 w-4 text-destructive" />
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Uninstall {plugin.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the plugin record and every surface it contributes. Any data the
            plugin stored elsewhere is not touched.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={remove.isPending}
            onClick={(e) => {
              e.preventDefault();
              remove.mutate();
            }}
          >
            {remove.isPending ? "Uninstalling…" : "Uninstall"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function PluginsPage() {
  const qc = useQueryClient();
  const [manifest, setManifest] = useState("");
  const [error, setError] = useState<string | null>(null);

  const { data: plugins, isLoading } = useQuery({ queryKey: ["plugins"], queryFn: listPlugins });

  const install = useMutation({
    mutationFn: () => installPlugin(manifest),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["plugins"] });
      setManifest("");
      setError(null);
    },
    // The backend returns the precise schema violation; showing it verbatim is
    // the difference between a fixable error and a guess.
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not install plugin"),
  });

  const toggle = useMutation({
    mutationFn: ({ name, enabled }: { name: string; enabled: boolean }) =>
      setPluginEnabled(name, enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["plugins"] }),
  });

  return (
    <div>
      <PageHeader
        title="Plugins"
        description="Extend CypherPanel with plugins, themes, and language packs. Installed plugins start disabled so you can review what they ask for first."
      />

      <Card>
        <CardContent className="grid gap-3 p-4">
          <div className="grid gap-1.5">
            <Label htmlFor="plugin-manifest">plugin.yaml</Label>
            <Textarea
              id="plugin-manifest"
              className="min-h-48 font-mono text-sm"
              spellCheck={false}
              value={manifest}
              onChange={(e) => setManifest(e.target.value)}
              placeholder={sampleManifest}
            />
            <p className="text-xs text-muted-foreground">
              Validated against manifest schema v1 before anything is recorded. Unknown
              fields are rejected rather than ignored.
            </p>
          </div>
          <div className="flex gap-2">
            <Button onClick={() => install.mutate()} disabled={!manifest.trim() || install.isPending}>
              <Upload className="h-4 w-4" /> {install.isPending ? "Validating…" : "Install"}
            </Button>
            <Button variant="outline" onClick={() => setManifest(sampleManifest)}>
              Use example
            </Button>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          {isLoading ? (
            <div className="space-y-2 p-4">
              {[0, 1].map((i) => (
                <Skeleton key={i} className="h-14 w-full" />
              ))}
            </div>
          ) : (plugins ?? []).length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              <Plug className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
              No plugins installed.
            </p>
          ) : (
            <div className="divide-y divide-border">
              {(plugins ?? []).map((p) => (
                <div key={p.name} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
                  <div className="min-w-0">
                    <p className="flex items-center gap-2 font-medium">
                      {p.name}
                      <Badge variant="secondary">v{p.version}</Badge>
                      {p.kind !== "plugin" && <Badge variant="secondary">{p.kind}</Badge>}
                    </p>
                    {p.manifest?.description && (
                      <p className="text-sm text-muted-foreground">{p.manifest.description}</p>
                    )}
                    {(p.manifest?.permissions ?? []).length > 0 && (
                      <p className="mt-1 text-xs text-muted-foreground">
                        Requests:{" "}
                        <span className="font-mono">
                          {(p.manifest?.permissions ?? []).join(", ")}
                        </span>
                      </p>
                    )}
                  </div>
                  <div className="flex items-center gap-3">
                    <Switch
                      checked={p.enabled}
                      aria-label={`${p.enabled ? "Disable" : "Enable"} ${p.name}`}
                      onCheckedChange={(enabled) => toggle.mutate({ name: p.name, enabled })}
                    />
                    <UninstallButton plugin={p} />
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
