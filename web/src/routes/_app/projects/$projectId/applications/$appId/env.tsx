// Application · Env vars: write-only values (ui-principles §6) — keys listed,
// values never returned. Changes apply on the next deploy.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getListEnvVarKeysQueryKey,
  useDeleteEnvVar,
  useListEnvVarKeys,
  useSetEnvVar,
} from "@/api/gen/applications/applications";
import { useDeployApplication } from "@/api/gen/deployments/deployments";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/env")({
  component: EnvTab,
});

function EnvTab() {
  const { projectId, appId } = Route.useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const keys = useListEnvVarKeys(appId);
  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");

  const invalidate = () => void qc.invalidateQueries({ queryKey: getListEnvVarKeysQueryKey(appId) });

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) =>
        void navigate({
          to: "/projects/$projectId/applications/$appId/deployments",
          params: { projectId, appId },
          search: { dep: d.id },
        }),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Deploy failed to start"),
    },
  });

  // A saved variable changes nothing until the container is replaced, so the
  // toast carries the verb that makes it true rather than leaving the operator
  // to find it (canvas 10c).
  const applied = {
    detail: "Applies on the next deploy",
    actions: [{ label: "Deploy now", onClick: () => deploy.mutate({ id: appId, data: {} }) }],
  };

  const setVar = useSetEnvVar({
    mutation: {
      onSuccess: () => {
        invalidate();
        setNewKey("");
        setNewValue("");
        toastSuccess({ title: "Env var saved", ...applied });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not save the variable"),
    },
  });
  const deleteVar = useDeleteEnvVar({
    mutation: {
      onSuccess: () => {
        invalidate();
        toastSuccess({ title: "Env var removed", ...applied });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not remove the variable"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (newKey.trim() === "") return;
    setVar.mutate({ id: appId, key: newKey.trim(), data: { value: newValue } });
  };

  return (
    <div className="max-w-xl space-y-4">
      <div>
        <Eyebrow>Env vars</Eyebrow>
        <p className="mt-1 text-[13px] text-text-mid">
          Injected into the container at deploy time. Values are sealed and write-only — they can be replaced, never read
          back.
        </p>
      </div>

      <PageState
        query={keys}
        isEmpty={(d) => d.keys.length === 0}
        empty={<EmptyState title="No env vars" hint="Anything your app reads from the environment — API keys, connection strings — goes here." />}
      >
        {(d) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {d.keys.map((k) => (
              <EnvRow
                key={k}
                name={k}
                onSave={(value) => setVar.mutate({ id: appId, key: k, data: { value } })}
                onDelete={() => deleteVar.mutate({ id: appId, key: k })}
              />
            ))}
          </ul>
        )}
      </PageState>

      <form onSubmit={submit} className="flex items-end gap-2">
        <div className="flex-1 space-y-1">
          <label htmlFor="new-env-key" className="eyebrow block">
            key
          </label>
          <Input
            id="new-env-key"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value.toUpperCase())}
            placeholder="DATABASE_URL"
            className="mono"
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <div className="flex-1 space-y-1">
          <label htmlFor="new-env-value" className="eyebrow block">
            value
          </label>
          <Input
            id="new-env-value"
            type="password"
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            className="mono"
            autoComplete="off"
          />
        </div>
        <Button type="submit" variant="primary" disabled={setVar.isPending || newKey.trim() === ""}>
          <Plus className="h-3.5 w-3.5" /> Add
        </Button>
      </form>
    </div>
  );
}

function EnvRow({ name, onSave, onDelete }: { name: string; onSave: (value: string) => void; onDelete: () => void }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");

  return (
    <li className="flex items-center justify-between gap-3 px-3 py-2">
      <span className="mono min-w-0 truncate text-[13px] text-text">{name}</span>
      <span className="flex shrink-0 items-center gap-1.5">
        {editing ? (
          <>
            <Input
              type="password"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="new value"
              className="mono h-7 w-44"
              autoFocus
            />
            <Button
              size="sm"
              variant="primary"
              onClick={() => {
                onSave(value);
                setEditing(false);
                setValue("");
              }}
            >
              Save
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </>
        ) : (
          <>
            <span className="mono text-xs text-text-faint">••••••••</span>
            <Button size="sm" variant="ghost" onClick={() => setEditing(true)}>
              Replace
            </Button>
            <Button size="sm" variant="ghost" aria-label={`Remove ${name}`} onClick={onDelete}>
              <Trash2 className="h-3.5 w-3.5 text-danger" />
            </Button>
          </>
        )}
      </span>
    </li>
  );
}
