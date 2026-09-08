// Compose stack · Variables: write-only values (ui-principles §6), keys listed
// and values never returned — the same contract an application's env vars keep,
// and the same row shape, because it is the same promise.
//
// What is different is where they land. Compose interpolates `${KEY}` from
// them, so a variable here is how a secret stays OUT of the stored file: sealed
// rows reach compose through an env file the agent writes 0600 and removes on
// every exit path (compose-stacks.md). That is the reason to use one rather
// than typing the value into the YAML, so the page says it.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useRef, useState, type FormEvent } from "react";
import {
  getListComposeEnvVarsQueryKey,
  useDeleteComposeEnvVar,
  useDeployComposeStack,
  useListComposeEnvVars,
  useSetComposeEnvVar,
} from "@/api/gen/compose-stacks/compose-stacks";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId/env")({
  component: ComposeEnvTab,
});

function ComposeEnvTab() {
  const { stackId } = Route.useParams();
  const qc = useQueryClient();
  const keys = useListComposeEnvVars(stackId);
  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");
  // The empty state's verb lands on the form already on the page rather than
  // opening a second one (15a — never a dead end).
  const keyField = useRef<HTMLInputElement>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: getListComposeEnvVarsQueryKey(stackId) });

  const deploy = useDeployComposeStack({
    mutation: {
      onSuccess: () => toastSuccess({ title: "Deploying", detail: "The agent re-converges the stack with the new variables." }),
      onError: (e: unknown, vars) => toastFailed("Could not deploy the stack", e, { retry: () => deploy.mutate(vars) }),
    },
  });

  // A saved variable changes nothing until the stack converges again, so the
  // toast carries the verb that makes it true (canvas 10c).
  const applied = {
    detail: "Applies on the next deploy",
    actions: [{ label: "Deploy now", onClick: () => deploy.mutate({ id: stackId }) }],
  };

  const setVar = useSetComposeEnvVar({
    mutation: {
      onSuccess: () => {
        invalidate();
        setNewKey("");
        setNewValue("");
        toastSuccess({ title: "Variable saved", ...applied });
      },
      onError: (e: unknown, vars) => toastFailed("Could not save the variable", e, { retry: () => setVar.mutate(vars) }),
    },
  });
  const deleteVar = useDeleteComposeEnvVar({
    mutation: {
      onSuccess: () => {
        invalidate();
        toastSuccess({ title: "Variable removed", ...applied });
      },
      onError: (e: unknown, vars) =>
        toastFailed("Could not remove the variable", e, { retry: () => deleteVar.mutate(vars) }),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (newKey.trim() === "") return;
    setVar.mutate({ id: stackId, key: newKey.trim(), data: { value: newValue } });
  };

  return (
    <div className="max-w-xl space-y-4">
      <div>
        <Eyebrow>Variables</Eyebrow>
        <p className="mt-1 text-[13px] leading-[1.5] text-text-mid">
          Compose interpolates <code className="mono text-[11.5px] text-text">{"${KEY}"}</code> in the file from these.
          Values are sealed and write-only — they can be replaced, never read back — and they never live in the stored
          file, so a password in the YAML is a password in the revision history.
        </p>
      </div>

      <PageState
        query={keys}
        isEmpty={(d) => d.keys.length === 0}
        empty={
          <EmptyState
            title="No variables"
            hint="Anything the file references as ${KEY} — passwords, tokens, an admin email — goes here rather than in the YAML."
            action={
              <Button variant="secondary" onClick={() => keyField.current?.focus()}>
                <Plus className="h-3.5 w-3.5" /> Add a variable
              </Button>
            }
          />
        }
      >
        {(d) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {d.keys.map((k) => (
              <EnvRow
                key={k}
                name={k}
                onSave={(value) => setVar.mutate({ id: stackId, key: k, data: { value } })}
                onDelete={() => deleteVar.mutate({ id: stackId, key: k })}
              />
            ))}
          </ul>
        )}
      </PageState>

      <form onSubmit={submit} className="flex items-end gap-2">
        <div className="flex-1 space-y-1">
          <label htmlFor="new-compose-key" className="eyebrow block">
            key
          </label>
          <Input
            ref={keyField}
            id="new-compose-key"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value.toUpperCase())}
            placeholder="GF_SECURITY_ADMIN_PASSWORD"
            className="mono"
            autoComplete="off"
            spellCheck={false}
          />
        </div>
        <div className="flex-1 space-y-1">
          <label htmlFor="new-compose-value" className="eyebrow block">
            value
          </label>
          <Input
            id="new-compose-value"
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
              aria-label={`New value for ${name}`}
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
