// New compose stack.
//
// SOURCING NOTE, same as `settings/audit.tsx`: a Compose Stack is slice 5 of
// web-ui-design.md §7 and has no card in the design canvas — turn 3 is past the
// 256 KiB read cap and nothing in the readable turns draws one. So this is not
// transcribed from a screen. It is built from the two create dialogs that DO
// have cards (9b's application, 10a's database): the same `size="form"` shell,
// the same accent eyebrow naming where the dialog is acting, the same
// always-stated placement, the same locked fieldset while the create is in
// flight. When turn 3 is readable, the diff should be a token check.
//
// What is specific to a stack is the file. The file IS the desired state
// (compose-stacks.md), so it is the body of the form rather than something
// configured afterwards, and the two directives the plane refuses are named
// beside it — a 400 five seconds after a paste is a worse way to learn that
// `build:` has nowhere to run than one line under the box.
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { getListComposeStacksQueryKey, useCreateComposeStack } from "@/api/gen/compose-stacks/compose-stacks";
import { useListServers } from "@/api/gen/servers/servers";
import { JoinServerFirstDialog } from "@/components/join-server-first-dialog";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select, Textarea } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

/** What a first stack starts from, so the box is never a blank the operator has
 *  to fill from memory. It is a whole valid file: paste-and-deploy works. */
const STARTER = `services:
  web:
    image: nginx:1.27-alpine
    restart: unless-stopped
`;

export function NewComposeStackDialog({
  envId,
  projectId,
  projectName,
  envName,
  primary,
}: {
  envId: string;
  projectId: string;
  projectName: string;
  envName: string;
  primary?: boolean;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const servers = useListServers();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [serverId, setServerId] = useState("");
  const [yaml, setYaml] = useState(STARTER);
  const [domain, setDomain] = useState("");
  const [service, setService] = useState("");
  const [port, setPort] = useState("");
  const [error, setError] = useState<string | null>(null);

  // A builder-only agent is built without a workload driver and rejects rollout
  // work; the API answers 400 for one, so it is not offered.
  const enrolled = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const chosenServer = serverId || enrolled[0]?.id || "";
  const serverName = enrolled.find((s) => s.id === chosenServer)?.name ?? "the server";

  const create = useCreateComposeStack({
    mutation: {
      onSuccess: (stack) => {
        // The board behind the popup is drawn from the cached list, so a stack
        // created here is invisible there until the list is asked for again.
        void qc.invalidateQueries({ queryKey: getListComposeStacksQueryKey(envId) });
        toastSuccess({
          title: `${stack.name} created`,
          detail: "Nothing is running yet — deploying is what ships the file.",
        });
        setOpen(false);
        void navigate({
          to: "/projects/$projectId/compose/$stackId",
          params: { projectId, stackId: stack.id },
        });
      },
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not create the stack");
        toastFailed("Could not create the stack", e, { retry: () => create.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(create);

  const trigger = (
    <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "md"}>
      <Plus className="h-3.5 w-3.5" aria-hidden /> New compose stack
    </Button>
  );

  if (!servers.isPending && enrolled.length === 0) {
    return <JoinServerFirstDialog trigger={trigger} resource="compose stack" />;
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      id: envId,
      data: {
        name: name.trim(),
        server_id: chosenServer,
        compose_yaml: yaml,
        // A route is all-or-nothing: a domain without the service and port
        // behind it cannot be turned into a Traefik fragment.
        ...(domain.trim()
          ? {
              route: {
                domain: domain.trim(),
                service: service.trim(),
                port: Number(port) || 0,
                https: true,
              },
            }
          : {}),
      },
    });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setError(null);
      }}
    >
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent
        size="form"
        eyebrow={
          <>
            {projectName} / {envName} / <span className="text-accent">new compose stack</span>
          </>
        }
        title="Create a compose stack"
      >
        <form onSubmit={submit} className="space-y-4">
          <fieldset disabled={create.isPending} className="min-w-0 space-y-4">
            <Field label="Name" qualifier="· what the board calls it">
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  maxLength={100}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="monitoring"
                />
              )}
            </Field>

            {/* Placement is always stated (9b) — a one-server fleet is still
                told which host it landed on. */}
            <Field label="Server">
              {(id) => (
                <Select id={id} value={chosenServer} onChange={(e) => setServerId(e.target.value)}>
                  {enrolled.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>

            <Field
              label="Compose file"
              qualifier="· this is the desired state"
              hint="Runs as written, with one fixed `docker compose up -d --remove-orphans --wait`. `build:` and `container_name:` are refused — there is no builder on a target host, and a fixed name collides across environments. Secrets belong in the stack's variables, not in the file."
              error={error ?? undefined}
            >
              {(id) => (
                <Textarea
                  id={id}
                  required
                  value={yaml}
                  onChange={(e) => setYaml(e.target.value)}
                  spellCheck={false}
                  className="min-h-56 text-[12px]"
                  aria-label="Compose file"
                />
              )}
            </Field>

            {/* The file's own Traefik labels cannot work — the managed Proxy
                runs the file provider only (ADR-004) — so the stack names which
                service answers and the plane emits the fragment itself. */}
            <Field
              label="Domain"
              qualifier="· optional"
              hint="Leave it empty and the stack publishes nothing through the Proxy; its services still reach each other, and anything the file publishes with `ports:` is published as the file asks."
            >
              {(id) => (
                <Input
                  id={id}
                  value={domain}
                  onChange={(e) => setDomain(e.target.value)}
                  placeholder="grafana.example.com"
                />
              )}
            </Field>

            {domain.trim() !== "" && (
              <div className="grid grid-cols-2 gap-3">
                <Field label="Service" qualifier="· which one answers">
                  {(id) => (
                    <Input
                      id={id}
                      required
                      value={service}
                      onChange={(e) => setService(e.target.value)}
                      placeholder="grafana"
                    />
                  )}
                </Field>
                <Field label="Port" qualifier="· inside its container">
                  {(id) => (
                    <Input
                      id={id}
                      required
                      inputMode="numeric"
                      value={port}
                      onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))}
                      placeholder="3000"
                    />
                  )}
                </Field>
              </div>
            )}
          </fieldset>

          <p className="text-[12px] leading-[1.5] text-text-faint">
            Creating it stores the file on {serverName}’s behalf; nothing starts until you deploy.
          </p>

          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={state}
              busyLabel="Creating…"
              successLabel="Created"
              disabledReason={name.trim() === "" ? "Name the stack first" : undefined}
            >
              Create stack →
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
