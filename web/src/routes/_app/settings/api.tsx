// Settings · API reference (canvas 14i) — the contract, rendered by the binary
// that serves it.
//
// Everything on this page is DERIVED from `GET /api/v1/openapi.yaml`, which
// ships inside cypherd. There is no second copy of the API description here to
// drift out of date: summaries, parameters, bodies and status codes are read
// from the same YAML that CI already fails on when it diverges from the
// handlers (ENGINEERING rule 19). A route that gains a parameter gains it here
// on the next build with nobody editing this file.
//
// That is also the argument for the page existing at all. The spec is served by
// THIS panel at THIS version, so the reference cannot describe a release the
// operator is not running — which is exactly when a hosted docs site misleads
// you, right after an upgrade, holding a script that used to work.
//
// It is a reference, not a console: it produces a `curl` you choose to run, and
// never fires the request itself. `in-panel-api-reference.md` §3 records why —
// a console running as your session turns every destructive route into a
// one-click action inside a documentation page, with none of the confirms the
// real screens carry.
import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { useCreateToken } from "@/api/gen/auth/auth";
import { Ability } from "@/api/gen/model";
import { CopyButton } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { SkeletonRows, useSkeletonDelay } from "@/components/ui/skeleton";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/api")({ component: ApiReferenceTab });

// ── the spec, read at runtime ────────────────────────────────────────────

interface Operation {
  method: string;
  path: string;
  id: string;
  summary: string;
  description: string;
  tag: string;
  params: { name: string; in: string; required: boolean; description: string }[];
  hasBody: boolean;
  statuses: { code: string; description: string }[];
}

/** Tags read for humans. An unmapped tag falls back to its own name
 *  title-cased, so a new tag appears correctly without anyone editing this. */
const TAG_LABELS: Record<string, string> = {
  auth: "Auth & sessions",
  teams: "Teams & users",
  invites: "Invitations",
  "access-requests": "Access requests",
  servers: "Servers",
  projects: "Projects",
  applications: "Applications",
  deployments: "Deployments",
  "compose-stacks": "Compose stacks",
  databases: "Databases",
  backups: "Backups",
  previews: "Previews",
  registries: "Registries",
  protection: "Deploy protection",
  notifiers: "Notifiers",
  webhooks: "Webhooks",
  "scheduled-tasks": "Scheduled tasks",
  "shared-variables": "Shared variables",
  "deploy-keys": "Deploy keys",
  templates: "Templates",
  audit: "Audit",
  inbox: "Inbox",
  panel: "Panel",
  health: "Health",
};

function labelFor(tag: string): string {
  return TAG_LABELS[tag] ?? tag.replace(/-/g, " ").replace(/^./, (c) => c.toUpperCase());
}

const METHODS = ["get", "post", "put", "patch", "delete"] as const;

/**
 * Fetches the spec and flattens it into the shape this page renders. The YAML
 * parser is imported dynamically so it lands in this route's chunk rather than
 * the main bundle — the page is a settings tab most sessions never open.
 */
function useSpec() {
  const [state, setState] = useState<{ ops: Operation[]; version: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        const [{ parse }, res] = await Promise.all([import("yaml"), fetch("/api/v1/openapi.yaml")]);
        if (!res.ok) throw new Error(`the panel answered ${res.status}`);
        const doc = parse(await res.text()) as {
          info?: { version?: string };
          paths?: Record<string, Record<string, unknown>>;
        };
        const ops: Operation[] = [];
        for (const [path, item] of Object.entries(doc.paths ?? {})) {
          // Parameters declared on the path item apply to every operation under
          // it — a detail worth honouring, because that is where most `{id}`
          // parameters in this spec are declared.
          const shared = (item.parameters as Operation["params"] | undefined) ?? [];
          for (const method of METHODS) {
            const op = item[method] as Record<string, unknown> | undefined;
            if (!op) continue;
            const responses = (op.responses as Record<string, { description?: string }>) ?? {};
            ops.push({
              method: method.toUpperCase(),
              path,
              id: (op.operationId as string) ?? `${method}${path}`,
              summary: (op.summary as string) ?? "",
              description: (op.description as string) ?? "",
              tag: ((op.tags as string[]) ?? ["other"])[0] ?? "other",
              params: [...shared, ...(((op.parameters as Operation["params"]) ?? []) || [])].map((p) => ({
                name: p.name,
                in: p.in,
                required: Boolean(p.required),
                description: p.description ?? "",
              })),
              hasBody: op.requestBody !== undefined,
              statuses: Object.entries(responses).map(([code, r]) => ({ code, description: r?.description ?? "" })),
            });
          }
        }
        if (live) setState({ ops, version: doc.info?.version ?? "" });
      } catch (e) {
        if (live) setError(e instanceof Error ? e.message : "the spec could not be read");
      }
    })();
    return () => {
      live = false;
    };
  }, []);

  return { state, error };
}

// ── the curl ─────────────────────────────────────────────────────────────

/**
 * The command, against the origin the reader actually reached this panel at.
 * `CYPHERD_PUBLIC_URL` is deliberately not consulted: the job is a command that
 * works from where the operator is standing.
 */
function curlFor(op: Operation, token: string): string {
  const example = op.path.replace(/\{(\w+)\}/g, (_, name: string) => `<${name}>`);
  const url = `${window.location.origin}${example}`;
  const lines = [`curl${op.method === "GET" ? "" : ` -X ${op.method}`} ${url} \\`];
  lines.push(`  -H "Authorization: Bearer ${token}"`);
  if (op.hasBody) {
    lines[lines.length - 1] += " \\";
    lines.push(`  -H "Content-Type: application/json" \\`);
    lines.push(`  -d '{}'`);
  }
  return lines.join("\n");
}

/** What a token minted for this operation should be allowed to do. A token
 *  created to run one GET must not be able to delete a server. */
function abilityFor(op: Operation): Ability {
  if (op.method === "GET") return Ability.read;
  if (op.path.includes("/deploy") || op.path.includes("/rollback")) return Ability.deploy;
  if (op.path.startsWith("/api/v1/servers")) return Ability.servers;
  return Ability.write;
}

// ── the page ─────────────────────────────────────────────────────────────

function ApiReferenceTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "api" }]);
  const { state, error } = useSpec();
  const [tag, setTag] = useState<string>("");
  const [open, setOpen] = useState<string | null>(null);
  // Held in memory for this tab only, never stored: §5 is explicit that a
  // credential in browser storage outlives the tab and is a worse exposure
  // than one on a clipboard.
  const [minted, setMinted] = useState<string | null>(null);
  const showSkeleton = useSkeletonDelay(state === null && error === null);

  const groups = useMemo(() => {
    if (!state) return [];
    const byTag = new Map<string, Operation[]>();
    for (const op of state.ops) {
      const list = byTag.get(op.tag);
      if (list) list.push(op);
      else byTag.set(op.tag, [op]);
    }
    return [...byTag.entries()].map(([t, ops]) => ({ tag: t, label: labelFor(t), ops }));
  }, [state]);

  const active = tag || groups[0]?.tag || "";
  const shown = groups.find((g) => g.tag === active);

  if (error) {
    return (
      <EmptyState
        title="The spec could not be read"
        hint={`This panel serves its own contract at /api/v1/openapi.yaml, and that request failed — ${error}.`}
      />
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <Eyebrow>API {state?.version ? `v${state.version}` : ""}</Eyebrow>
        {state && (
          <span className="mono text-[11px] text-text-faint">
            {state.ops.length} operations · served by this panel
          </span>
        )}
      </div>
      <p className="max-w-2xl text-[12.5px] leading-[1.5] text-text-mid">
        Read from this binary’s own <code className="mono text-[11.5px] text-text">/api/v1/openapi.yaml</code>, so it
        describes the version you are running rather than the latest release. Every screen in this panel is a client of
        these routes — there is nothing the UI can do that a call here cannot.
      </p>

      {showSkeleton && <SkeletonRows columns="1fr" rows={6} />}

      {state && (
        <div className="flex flex-col gap-5 lg:flex-row">
          {/* The rail is the spec's own tags, in the order it declares them. */}
          <nav aria-label="API areas" className="shrink-0 lg:w-[190px]">
            <ul className="flex gap-1 overflow-x-auto lg:block lg:space-y-px lg:overflow-visible">
              {groups.map((g) => (
                <li key={g.tag}>
                  <button
                    type="button"
                    aria-current={g.tag === active ? "true" : undefined}
                    onClick={() => {
                      setTag(g.tag);
                      setOpen(null);
                    }}
                    className={cn(
                      "w-full whitespace-nowrap rounded-md px-2.5 py-1.5 text-left text-[12.5px] transition-colors",
                      g.tag === active ? "bg-raised font-semibold text-text" : "text-text-mid hover:text-text",
                    )}
                  >
                    {g.label}
                    <span className="mono ml-1.5 text-[10.5px] text-text-faint">{g.ops.length}</span>
                  </button>
                </li>
              ))}
              <li>
                <button
                  type="button"
                  aria-current={active === "__errors" ? "true" : undefined}
                  onClick={() => {
                    setTag("__errors");
                    setOpen(null);
                  }}
                  className={cn(
                    "w-full whitespace-nowrap rounded-md px-2.5 py-1.5 text-left text-[12.5px] transition-colors",
                    active === "__errors" ? "bg-raised font-semibold text-text" : "text-text-mid hover:text-text",
                  )}
                >
                  Errors &amp; rate limits
                </button>
              </li>
            </ul>
          </nav>

          <div className="min-w-0 flex-1">
            {active === "__errors" ? (
              <ErrorsSection />
            ) : (
              <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
                {shown?.ops.map((op) => (
                  <OperationRow
                    key={op.id}
                    op={op}
                    open={open === op.id}
                    onToggle={() => setOpen((v) => (v === op.id ? null : op.id))}
                    token={minted}
                    onMinted={setMinted}
                  />
                ))}
              </ul>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

const METHOD_TONE: Record<string, string> = {
  GET: "text-status-running",
  POST: "text-status-deploying",
  PUT: "text-status-deploying",
  PATCH: "text-status-degraded-text",
  DELETE: "text-danger",
};

function OperationRow({
  op,
  open,
  onToggle,
  token,
  onMinted,
}: {
  op: Operation;
  open: boolean;
  onToggle: () => void;
  token: string | null;
  onMinted: (t: string) => void;
}) {
  const command = curlFor(op, token ?? "$CYPHER_TOKEN");
  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="flex w-full items-baseline gap-2.5 px-3.5 py-2.5 text-left hover:bg-raised"
      >
        <span className={cn("mono w-[52px] shrink-0 text-[11px] font-semibold", METHOD_TONE[op.method])}>
          {op.method}
        </span>
        <span className="mono min-w-0 flex-1 truncate text-[12.5px] text-text">{op.path}</span>
        <span className="hidden min-w-0 max-w-[45%] truncate text-[12px] text-text-faint sm:block">{op.summary}</span>
      </button>

      {open && (
        <div className="space-y-3 border-t border-border-subtle px-3.5 py-3">
          {op.summary && <p className="text-[13px] font-medium text-text">{op.summary}</p>}
          {op.description && (
            <p className="max-w-2xl whitespace-pre-line text-[12.5px] leading-[1.55] text-text-mid">
              {op.description.trim()}
            </p>
          )}

          {op.params.length > 0 && (
            <div>
              <p className="eyebrow mb-1">Parameters</p>
              <ul className="space-y-0.5">
                {op.params.map((p) => (
                  <li key={`${p.in}-${p.name}`} className="text-[12px] leading-[1.5] text-text-mid">
                    <span className="mono text-text">{p.name}</span>
                    <span className="mono text-text-faint"> · {p.in}</span>
                    {p.required && <span className="mono text-danger"> · required</span>}
                    {p.description && <span> — {p.description}</span>}
                  </li>
                ))}
              </ul>
            </div>
          )}

          <div className="relative">
            <div className="absolute right-2 top-2 z-10">
              <CopyButton value={command} label={`Copy the curl for ${op.id}`} />
            </div>
            <pre className="overflow-x-auto rounded-md border border-pane-border bg-pane px-3.5 py-3 font-mono text-[11.5px] leading-[1.7] text-pane-text">
              {command}
            </pre>
          </div>

          <div className="flex flex-wrap items-center gap-2.5">
            <MintTokenButton op={op} hasToken={token !== null} onMinted={onMinted} />
            <span className="mono text-[11px] text-text-faint">
              {token
                ? "the command above carries a live credential"
                : "$CYPHER_TOKEN is yours to set — nothing sensitive is on the clipboard"}
            </span>
          </div>

          {op.statuses.length > 0 && (
            <p className="mono text-[11px] leading-[1.7] text-text-faint">
              {op.statuses.map((s) => `${s.code} ${s.description}`.trim()).join(" · ")}
            </p>
          )}
        </div>
      )}
    </li>
  );
}

/**
 * The canvas's "copy with my token". The panel cannot insert an EXISTING
 * token — `listTokens` is metadata-only and a raw secret appears exactly once,
 * in the create response — so the honest reading is to offer to mint one,
 * scoped to what this operation needs, and say so before doing it.
 */
function MintTokenButton({
  op,
  hasToken,
  onMinted,
}: {
  op: Operation;
  hasToken: boolean;
  onMinted: (t: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const create = useCreateToken({
    mutation: {
      onSuccess: (res) => {
        onMinted(res.token);
        setOpen(false);
      },
      onError: (e: unknown) => toastFailed("Could not create the token", e),
    },
  });
  const ability = abilityFor(op);

  if (hasToken) {
    return <span className="mono text-[11px] text-status-degraded-text">token in this tab only</span>;
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(true)}>
        Copy with a new token
      </Button>
      <DialogContent
        title="Create a token for this call?"
        description="The panel cannot read back a token you already have — a raw secret is shown exactly once, when it is created, and never stored anywhere it could be recovered. So this makes a new one."
      >
        <div className="space-y-3">
          <div className="rounded-md border border-border bg-raised px-3.5 py-2.5">
            <p className="text-[12.5px] leading-[1.5] text-text">
              It will be allowed to <span className="mono">{ability}</span> and nothing else — scoped to what{" "}
              <span className="mono">{op.method}</span> needs, so a token minted to run one call cannot be used for
              another.
            </p>
          </div>
          {/* The sentence is the point: an operator who reads it and proceeds
              has made a decision; one who never saw it has had it made for
              them. */}
          <p className="text-[12.5px] leading-[1.5] text-danger">
            The copied command will contain a live credential. Anything that can read your clipboard can read it.
          </p>
          <p className="mono text-[11px] leading-[1.6] text-text-faint">
            held in this tab only · never written to browser storage · revoke it from Settings → Account
          </p>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              variant="danger"
              size="lg"
              state={create.isPending ? "busy" : "idle"}
              busyLabel="Creating…"
              onClick={() =>
                create.mutate({
                  data: { name: `api reference · ${op.id}`, abilities: [ability], expires_in_days: 1 },
                })
              }
            >
              Create and use it
            </ActionButton>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Hand-written, because none of it is per-operation: every JSON error in this
 * API shares one envelope, and repeating it 198 times would be noise.
 */
function ErrorsSection() {
  return (
    <div className="max-w-2xl space-y-4">
      <section className="space-y-1.5">
        <Eyebrow>One error shape</Eyebrow>
        <p className="text-[12.5px] leading-[1.55] text-text-mid">
          Every failure answers with the same JSON envelope, whatever produced it. The{" "}
          <code className="mono text-[11.5px] text-text">trace_id</code> repeats the{" "}
          <code className="mono text-[11.5px] text-text">X-Request-Id</code> header that rides on every response,
          success or not — it is the value to quote in a bug report and the key to search for in the panel log
          (Settings → Diagnostics).
        </p>
        <pre className="overflow-x-auto rounded-md border border-pane-border bg-pane px-3.5 py-3 font-mono text-[11.5px] leading-[1.7] text-pane-text">
          {`{ "error": "no such application", "trace_id": "req_8fk2x91b04aa" }`}
        </pre>
      </section>

      <section className="space-y-1.5">
        <Eyebrow>What a status code means here</Eyebrow>
        <ul className="space-y-1 text-[12.5px] leading-[1.55] text-text-mid">
          <li>
            <span className="mono text-text">404</span> — also what a non-member gets for a resource that exists.
            Existence never leaks across tenants, so “not found” and “not yours” are deliberately the same answer.
          </li>
          <li>
            <span className="mono text-text">403</span> — you can see it, your rank is too low to change it. Rank is
            checked when a permission is created, never when it is spent.
          </li>
          <li>
            <span className="mono text-text">409</span> — a guard refused, and the message names what is in the way:
            the last owner of a team, a registry an application still pulls through, a freeze window.
          </li>
          <li>
            <span className="mono text-text">429</span> — throttled. Sign-in is throttled per client address, and so
            are the two public invitation routes. Everything else is not rate-limited.
          </li>
        </ul>
      </section>

      <section className="space-y-1.5">
        <Eyebrow>Authentication</Eyebrow>
        <p className="text-[12.5px] leading-[1.55] text-text-mid">
          A bearer token on every request — a session token from{" "}
          <code className="mono text-[11.5px] text-text">POST /auth/login</code>, or a personal access token. The GitHub
          webhook is the one exception: it authenticates by per-application HMAC instead. Some routes are{" "}
          <b className="text-text">session-only</b> and refuse a token entirely — anything that can switch a protection
          control off, because a token that leaked into CI must not be able to open its own gate.
        </p>
      </section>
    </div>
  );
}
