// Settings · Registries (canvas 6d, dark 13d). A container registry credential
// is optional by construction — ADR-008's whole premise is that no registry is
// required — so the page leads with that rather than presenting an empty list
// as something missing. The copy is the canvas's: single-server builds stay in
// the local daemon, multi-server images travel over the mTLS relay, and a
// registry exists only for a private base image or to push builds somewhere the
// operator already runs.
//
// It shares the credential-row shape every other such surface uses (canvas 13a,
// 13d): a bordered list whose rows carry a bold label, a faint mono sub-line
// and a plain word action on the right. The sub-line is where the canvas puts
// the three facts that decide whether a row is safe to delete — who it
// authenticates as, that the token is sealed, and what it is allowed to do.
import { createFileRoute } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import {
  getListRegistriesQueryKey,
  useCreateRegistry,
  useDeleteRegistry,
  useListRegistries,
  useListRegistryUses,
  useTestRegistry,
  useTestRegistryConfig,
  useUpdateRegistry,
} from "@/api/gen/registries/registries";
import type { Registry } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/registries")({ component: RegistriesTab });

/** The plain word-action that ends a credential row (canvas 13a/13d). */
const rowAction = "shrink-0 text-[12px] font-medium hover:underline disabled:no-underline disabled:opacity-50";

function RegistriesTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "registries" }]);
  const registries = useListRegistries();
  return (
    <div className="max-w-xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Registries</Eyebrow>
        <AddRegistryDialog />
      </div>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        None required: single-server builds stay in the local daemon, multi-server images travel over the mTLS relay.
        Add one only for private base images or to push builds to your own registry.
      </p>
      <PageState
        query={registries}
        empty={
          <EmptyState
            title="No registries"
            hint="Nothing needs one — deploys work without a registry. Add one for a private base image, or to push builds somewhere you already run."
            action={<AddRegistryDialog primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((r) => (
              <RegistryRow key={r.id} r={r} />
            ))}
          </ul>
        )}
      </PageState>
      <p className="text-[12px] leading-[1.5] text-text-faint">
        When one exists, app settings gain “Push image after build”. Credentials are write-only, like env vars.
      </p>
    </div>
  );
}

function RegistryRow({ r }: { r: Registry }) {
  // What the credential may do, in the canvas's words. It decides whether the
  // row can be a build's push target at all, so it sits in the sub-line rather
  // than behind a click.
  const scope = r.can_pull && r.can_push ? "pull + push" : r.can_push ? "push only" : "pull only";
  return (
    <li className="px-4 py-3">
      <div className="flex items-center gap-3">
        <span className="min-w-0 flex-1">
          <span className="mono block truncate text-[13px] font-semibold text-text">{r.url}</span>
          <span className="mono mt-0.5 block truncate text-[11.5px] text-text-faint">
            {r.username ? `user ${r.username} · ` : ""}token sealed ····· · {scope}
          </span>
        </span>
        <TestRegistryAction r={r} />
        <EditRegistryDialog r={r} />
        <DeleteRegistryDialog r={r} />
      </div>
      <UsedBy r={r} />
    </li>
  );
}

// "used by 2 apps" / "unused" (canvas 13d). It is the blast radius of the
// delete beside it, so it is on the row rather than a click deeper — and the
// delete refuses server-side while anything still uses it.
function UsedBy({ r }: { r: Registry }) {
  const uses = useListRegistryUses(r.id);
  if (uses.isPending || uses.isError) return null;
  const n = uses.data?.length ?? 0;
  return (
    <p className="mono mt-1.5 text-[11px] text-text-faint">
      {n === 0 ? "unused" : `used by ${n} app${n === 1 ? "" : "s"}`}
      {n > 0 && (
        <span className="text-text-faint">
          {" · "}
          {uses.data?.map((u) => u.application_name).join(", ")}
        </span>
      )}
    </p>
  );
}

// Proving a credential before something depends on it. A rejected credential is
// a 200 with ok:false — the request succeeded, the authentication did not — so
// the failure is reported as the registry's own words rather than as an error.
function TestRegistryAction({ r }: { r: Registry }) {
  const qc = useQueryClient();
  const test = useTestRegistry({
    mutation: {
      onSuccess: (res) => {
        if (res.ok) toastSuccess(res.detail || `Authenticated to ${r.url}`);
        else toastFailed(res.detail || "The registry rejected these credentials", null);
        void qc.invalidateQueries({ queryKey: getListRegistriesQueryKey() });
      },
      onError: (e: unknown) => toastFailed("Could not reach the registry", e),
    },
  });
  return (
    <ActionButton
      type="button"
      className={cn(rowAction, "text-text-mid")}
      state={test.isPending ? "busy" : "idle"}
      busyLabel="Testing…"
      onClick={() => test.mutate({ id: r.id })}
      aria-label={`Test the connection to ${r.name}`}
    >
      Test
    </ActionButton>
  );
}

// Deleting a credential applications depend on would break their next deploy at
// the moment nobody is looking, so the panel refuses it and names them. The
// dialog says so up front rather than letting the operator discover it as a 409.
function DeleteRegistryDialog({ r }: { r: Registry }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const del = useDeleteRegistry({
    mutation: {
      onSuccess: () => {
        toastSuccess(`Deleted ${r.name}`);
        void qc.invalidateQueries({ queryKey: getListRegistriesQueryKey() });
        setOpen(false);
      },
      onError: (e: unknown, vars) =>
        toastFailed("Could not delete the registry", e, { retry: () => del.mutate(vars) }),
    },
  });
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button type="button" className={cn(rowAction, "text-danger")} aria-label={`Delete ${r.name}`}>
          Delete
        </button>
      </DialogTrigger>
      <DialogContent
        title={`Delete ${r.name}?`}
        description="Applications still using this registry keep it: the panel refuses the delete and names them. Detach it in each app’s settings first."
      >
        <div className="flex items-center justify-end gap-2.5">
          <DialogClose asChild>
            <Button variant="ghost" size="lg">
              Cancel
            </Button>
          </DialogClose>
          <ActionButton
            variant="danger"
            size="lg"
            state={del.isPending ? "busy" : "idle"}
            busyLabel="Deleting…"
            onClick={() => del.mutate({ id: r.id })}
          >
            Delete registry
          </ActionButton>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// Editing one. Everything is editable except the fact that the token is
// write-only: the field starts empty and an empty field keeps what is sealed,
// which is the API's own contract for an omitted token. That is what makes
// "change the scope" a thing you can do without re-typing a credential you no
// longer have a copy of.
//
// Rotating IS this dialog rather than a route of its own, because rotation is
// what typing into the token field means — the old value is replaced wholesale
// and nothing is cached, so the next deploy uses the new one and no builder is
// left holding a docker login.
function EditRegistryDialog({ r }: { r: Registry }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [canPull, setCanPull] = useState(r.can_pull);
  const [canPush, setCanPush] = useState(r.can_push);
  const [error, setError] = useState<string | null>(null);

  const update = useUpdateRegistry({
    mutation: {
      onSuccess: (next) => {
        toastSuccess(`Saved ${next.name}`);
        void qc.invalidateQueries({ queryKey: getListRegistriesQueryKey() });
        setOpen(false);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the registry"),
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    const f = new FormData(e.currentTarget);
    const token = String(f.get("token") ?? "");
    update.mutate({
      id: r.id,
      data: {
        name: String(f.get("name") ?? "").trim(),
        url: String(f.get("url") ?? "").trim(),
        username: String(f.get("username") ?? "").trim(),
        ...(token ? { token } : {}),
        can_pull: canPull,
        can_push: canPush,
      },
    });
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setError(null);
          setCanPull(r.can_pull);
          setCanPush(r.can_push);
        }
      }}
    >
      <DialogTrigger asChild>
        <button type="button" className={cn(rowAction, "text-text-mid")} aria-label={`Edit ${r.name}`}>
          Edit
        </button>
      </DialogTrigger>
      <DialogContent title={`Edit ${r.name}`}>
        <form onSubmit={onSubmit} className="space-y-3">
          <Field label="Name">
            {(id) => <Input id={id} name="name" required maxLength={100} defaultValue={r.name} autoFocus />}
          </Field>
          <Field label="Host" qualifier="· no scheme">
            {(id) => <Input id={id} name="url" required className="mono" defaultValue={r.url} />}
          </Field>
          <Field label="Username" qualifier="· empty for a bearer-token registry">
            {(id) => <Input id={id} name="username" className="mono" defaultValue={r.username ?? ""} />}
          </Field>
          <Field label="Token" qualifier="· empty keeps the sealed one" error={error ?? undefined}>
            {(id, describedBy) => (
              <Input
                id={id}
                name="token"
                type="password"
                className="mono"
                aria-describedby={describedBy}
                placeholder="•••••  (unchanged)"
              />
            )}
          </Field>
          {/* Scope is checked when the registry is ATTACHED, not when it is
              spent, so narrowing it here does not break a deploy already
              running — it refuses the next attach. */}
          <div className="space-y-2 pt-0.5">
            <label className="flex items-center gap-2.5">
              <input
                type="checkbox"
                checked={canPull}
                onChange={(e) => setCanPull(e.currentTarget.checked)}
                className="size-3.5 accent-accent"
              />
              <span className="text-[12.5px] text-text-mid">Allow pulling images through it</span>
            </label>
            <label className="flex items-center gap-2.5">
              <input
                type="checkbox"
                checked={canPush}
                onChange={(e) => setCanPush(e.currentTarget.checked)}
                className="size-3.5 accent-accent"
              />
              <span className="text-[12.5px] text-text-mid">Allow pushing builds here</span>
            </label>
          </div>
          <div className="flex justify-end gap-2 pt-1">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={update.isPending ? "busy" : "idle"}
              busyLabel="Saving…"
            >
              Save
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// Adding one. The token is write-only from the moment it is typed — it is
// sealed before it reaches storage and no route ever returns it — so the field
// says so rather than implying it can be read back later.
//
// Test proves the credential BEFORE it is stored. That matters more here than
// on most credential screens: a registry that rejects a login fails a deploy
// five minutes into a build, and the operator finds out from a build log.
function AddRegistryDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [canPush, setCanPush] = useState(false);
  const [url, setUrl] = useState("");
  const [username, setUsername] = useState("");
  const [token, setToken] = useState("");
  const [probe, setProbe] = useState<{ ok: boolean; detail: string } | null>(null);
  // A private-network registry cannot be tested from the panel at all — the
  // agent that does the pulling is on that network and the plane is not — and
  // the API says so in `detail` rather than reporting a failure. So the result
  // is rendered as the registry's own words either way.
  const test = useTestRegistryConfig({
    mutation: {
      onSuccess: (res) => setProbe({ ok: res.ok, detail: res.detail || (res.ok ? "Authenticated." : "Rejected.") }),
      onError: (e: unknown) =>
        setProbe({ ok: false, detail: e instanceof Error ? e.message : "The panel could not reach that host." }),
    },
  });
  const create = useCreateRegistry({
    mutation: {
      onSuccess: (r) => {
        toastSuccess(`Added ${r.name}`);
        void qc.invalidateQueries({ queryKey: getListRegistriesQueryKey() });
        setOpen(false);
        setCanPush(false);
        setProbe(null);
      },
      onError: (e: unknown) => toastFailed("Could not add the registry", e),
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    create.mutate({
      data: {
        name: String(f.get("name") ?? "").trim(),
        url: url.trim(),
        username: username.trim() || undefined,
        token,
        can_pull: true,
        can_push: canPush,
      },
    });
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size="sm">
          Add registry
        </Button>
      </DialogTrigger>
      <DialogContent title="Add registry">
        <form onSubmit={onSubmit} className="space-y-3">
          <Field label="Name" qualifier="· what you pick it by">
            {(id) => <Input id={id} name="name" required maxLength={100} placeholder="ghcr" autoFocus />}
          </Field>
          {/* No scheme: a registry reference carries none, and accepting one
              would produce image names nothing can pull. */}
          <Field label="Host" qualifier="· no scheme — ghcr.io, ghcr.io/acme, registry:5000">
            {(id) => (
              <Input
                id={id}
                name="url"
                required
                className="mono"
                placeholder="ghcr.io/acme"
                value={url}
                onChange={(e) => {
                  setUrl(e.target.value);
                  setProbe(null);
                }}
              />
            )}
          </Field>
          <Field label="Username" qualifier="· empty for a bearer-token registry">
            {(id) => (
              <Input
                id={id}
                name="username"
                className="mono"
                placeholder="meridian-bot"
                value={username}
                onChange={(e) => {
                  setUsername(e.target.value);
                  setProbe(null);
                }}
              />
            )}
          </Field>
          <Field label="Token" qualifier="· sealed on the way in, never shown again">
            {(id) => (
              <Input
                id={id}
                name="token"
                type="password"
                required
                className="mono"
                placeholder="•••••"
                value={token}
                onChange={(e) => {
                  setToken(e.target.value);
                  setProbe(null);
                }}
              />
            )}
          </Field>
          {probe && (
            <p
              role="status"
              className={cn(
                "rounded-md border px-3 py-2 text-[12.5px] leading-[1.5]",
                probe.ok
                  ? "border-status-running/35 bg-status-running/[0.06] text-status-running"
                  : "border-status-degraded/40 bg-status-degraded/[0.06] text-status-degraded-text",
              )}
            >
              {probe.detail}
            </p>
          )}
          {/* Pull is always on: it is the common case and the reason most
              credentials exist. Push is the larger grant, so it is opt-in and
              says what it unlocks. */}
          <label className="flex items-start gap-2.5 pt-0.5">
            <input
              type="checkbox"
              name="can_push"
              checked={canPush}
              onChange={(e) => setCanPush(e.currentTarget.checked)}
              className="mt-0.5 size-3.5 accent-accent"
            />
            <span className="text-[12.5px] leading-[1.45] text-text-mid">
              Allow pushing builds here
              <span className="block text-[11.5px] text-text-faint">
                Off by default. A pull-only credential named as a push target is refused when it is attached, not
                mid-deploy.
              </span>
            </span>
          </label>
          <div className="flex justify-end gap-2 pt-1">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="button"
              variant="secondary"
              size="lg"
              state={test.isPending ? "busy" : "idle"}
              busyLabel="Testing…"
              disabledReason={url.trim() === "" || token === "" ? "Fill in the host and token first" : undefined}
              onClick={() => {
                setProbe(null);
                test.mutate({ data: { url: url.trim(), username: username.trim() || undefined, token } });
              }}
            >
              ↗ Test
            </ActionButton>
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={create.isPending ? "busy" : "idle"}
              busyLabel="Adding…"
            >
              Add registry
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
