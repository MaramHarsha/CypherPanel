// Servers — the fleet, plus the join flow. Mission Control sets the fleet as a
// broadsheet table, the same shape the projects landing uses (canvas 1a): an
// ink rule above the first row, hairlines between the rest, a status column
// that can be read from across the room, and the machine facts in mono. The two
// top-level lists in the product should not be two different objects.
//
// The join flow is the first step of the chained golden path (ui-principles
// §11) and web-ui-design §4 states its terms exactly: the `curl | sh` command
// front and centre, a copy button, and honest progress until the agent is
// running. It lives in one dialog owned by this page — see the note on
// `joinOpen` for why it cannot live inside the rows it creates.
import { createFileRoute, Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import type { JoinInstructions, Server } from "@/api/gen/model";
import { useCreateServer, useListServers } from "@/api/gen/servers/servers";
import { CopyButton } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { HeaderStat, PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, StatusPill } from "@/components/status-badge";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime, absoluteTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/servers/")({ component: ServersPage });

// Below `sm` the table collapses to a stacked card (ui-principles §9), and the
// mono column header goes with it — headers over a stack are noise.
const GRID = "flex flex-col gap-2 sm:grid sm:grid-cols-[2fr_1.2fr_1fr] sm:items-center sm:gap-4";

function ServersPage() {
  useCrumbs([{ label: "servers" }]);
  // Server heartbeats are not on the invalidate stream — poll honestly.
  const servers = useListServers({ query: { refetchInterval: 5_000 } });
  const list = servers.data ?? [];
  const online = list.filter((s) => s.enrolled && s.status === "running").length;
  // A host that has never joined is pending, not broken; only an enrolled agent
  // that stopped reporting healthy makes the fleet count go red.
  const troubled = list.some((s) => s.enrolled && s.status !== "running");

  // One dialog for the whole page, deliberately. It used to be rendered inside
  // the empty state and inside the join card — both of which this page's own
  // poll can unmount. Creating the first server made `list` non-empty, the
  // empty state disappeared on the next 5-second refetch, and the open dialog
  // went with it: the join command vanished seconds after being shown, right
  // as the operator was copying it.
  const [joinOpen, setJoinOpen] = useState(false);

  return (
    <>
      <JoinServerDialog open={joinOpen} onOpenChange={setJoinOpen} />
      <PageHeader
        title="Servers"
        actions={
          <>
            {list.length > 0 && (
              <HeaderStat value={`${online}/${list.length}`} label="online" tone={troubled ? "error" : "default"} />
            )}
            <Button variant="primary" size="lg" onClick={() => setJoinOpen(true)}>
              <Plus className="h-3.5 w-3.5" /> Join a server
            </Button>
          </>
        }
      />
      {/* 1a hangs the first row close under the masthead rule — the ink line
          above it is the separator, so a full band would read as a gap. */}
      <PageBody className="pt-2 pb-9">
        <PageState
          query={servers}
          skeletonColumns="2fr 1.2fr 1fr"
          skeletonDot
          empty={
            <EmptyState
              emphasis
              title="Join your first server"
              hint="A server is any Linux host that runs your apps. Joining installs the CypherPanel agent with one copy-paste command — it dials home over mTLS, so there are no SSH keys to store and no ports to open."
              action={
                <Button variant="primary" size="lg" onClick={() => setJoinOpen(true)}>
                  <Plus className="h-3.5 w-3.5" /> Join a server
                </Button>
              }
            />
          }
        >
          {(rows) => (
            <div>
              <div className={cn(GRID, "eyebrow hidden px-2 pb-2.5 sm:grid")}>
                <span>Server</span>
                <span>Status</span>
                <span>Last heartbeat</span>
              </div>
              <ul>
                {rows.map((s, i) => (
                  <ServerRow key={s.id} server={s} first={i === 0} />
                ))}
              </ul>
              <JoinInvitation onJoin={() => setJoinOpen(true)} />
            </div>
          )}
        </PageState>
      </PageBody>
    </>
  );
}

function ServerRow({ server: s, first }: { server: Server; first: boolean }) {
  const status = s.enrolled ? normalizeStatus(s.status) : "unknown";
  // The machine facts, in the order an operator reads them: which box, what it
  // runs workloads with, which agent, and — only when it is not the default —
  // what this host is allowed to do (builder-role-and-relay.md §1).
  const facts = [s.hostname || s.id, s.driver, `agent ${s.agent_version}`, s.role && s.role !== "all" ? s.role : null]
    .filter(Boolean)
    .join(" · ");

  return (
    <li>
      <Link
        to="/servers/$serverId"
        params={{ serverId: s.id }}
        className={cn(
          GRID,
          "rounded-md border-t px-2 py-5 hover:bg-raised",
          first ? "border-t-[1.5px] border-border-strong" : "border-border",
          status === "error" && "bg-linear-to-r from-status-error/[0.06] to-transparent to-60%",
        )}
      >
        <span className="flex min-w-0 items-center gap-3.5">
          <StatusDot status={status} />
          <span className="min-w-0">
            <span className="block truncate text-[19px] font-semibold tracking-tight">{s.name}</span>
            <span className="mt-0.5 block truncate font-mono text-[11.5px] text-text-faint">
              {s.enrolled ? facts : "no agent has dialed home yet"}
            </span>
          </span>
        </span>

        <span className="min-w-0">
          <StatusPill status={status}>{s.enrolled ? status : "waiting to join"}</StatusPill>
        </span>

        <span className="font-mono text-xs text-text-dim">
          {s.last_seen_at ? <span title={absoluteTime(s.last_seen_at)}>{relativeTime(s.last_seen_at)}</span> : "never"}
        </span>
      </Link>
    </li>
  );
}

/**
 * The row that closes the table. "Add a server" stays a place on the page
 * rather than a button you have to find, and it carries the one plain-language
 * line the concept needs (ui-principles §11) — which the header pill, being a
 * pill, cannot.
 */
function JoinInvitation({ onJoin }: { onJoin: () => void }) {
  return (
    <div className="mt-4 flex flex-col gap-3.5 rounded-md border border-dashed border-border-input px-4 py-4 sm:flex-row sm:items-center sm:gap-5">
      <div className="min-w-0">
        <p className="text-[13px] font-semibold text-text">Join another server</p>
        <p className="mt-1 text-[12px] leading-[1.55] text-text-faint">
          One command on any Linux host. The agent dials home over mTLS — CypherPanel stores no SSH keys and opens no
          port on your server.
        </p>
      </div>
      <Button variant="secondary" size="md" className="shrink-0 self-start sm:ml-auto" onClick={onJoin}>
        Join a server
      </Button>
    </div>
  );
}

function JoinServerDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [name, setName] = useState("");
  const [join, setJoin] = useState<{ serverId: string; instructions: JoinInstructions } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateServer({
    mutation: {
      onSuccess: (res) => setJoin({ serverId: res.server.id, instructions: res.join }),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the server"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name } });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setJoin(null);
          setName("");
          setError(null);
          create.reset();
        }
        onOpenChange(next);
      }}
    >
      {join === null ? (
        <DialogContent
          title="Join a server"
          description="Name the host first — the join command is generated for it, valid once, and expires shortly."
        >
          <form onSubmit={submit} className="space-y-4">
            {/* A server name is a machine handle you will type again in deploy
                targets and logs, so it keeps the mono default. */}
            <Field label="Name" qualifier="· how you'll recognise this host" error={error ?? undefined}>
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="hetzner-1"
                />
              )}
            </Field>
            <div className="flex items-center justify-end gap-2.5 pt-1">
              <DialogClose asChild>
                <Button variant="ghost" size="lg">
                  Cancel
                </Button>
              </DialogClose>
              <ActionButton
                type="submit"
                variant="accent"
                size="lg"
                state={create.isPending ? "busy" : "idle"}
                busyLabel="Generating…"
                disabledReason={name.trim() === "" ? "Name the server first" : undefined}
              >
                Get join command →
              </ActionButton>
            </div>
          </form>
        </DialogContent>
      ) : (
        <DialogContent
          size="form"
          eyebrow={
            <>
              servers / join / <span className="text-accent">{name}</span>
            </>
          }
          title={`Run this on ${name}`}
          description="As root, on the host itself. The agent installs, dials home over mTLS, and appears in the fleet — usually within a minute."
        >
          <JoinProgress
            serverId={join.serverId}
            command={join.instructions.install_command}
            fingerprint={join.instructions.ca_fingerprint}
          />
        </DialogContent>
      )}
    </Dialog>
  );
}

/** Canvas 10a's step marks — the same three everywhere work is in flight. */
const STEP_MARK = {
  done: { glyph: "✓", className: "text-status-running" },
  active: { glyph: "▸", className: "text-status-deploying" },
  pending: { glyph: "○", className: "text-text-disabled" },
} as const;

/** Whole seconds: the join is watched by a 3s poll, so tenths would be theatre. */
function elapsedLabel(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

function JoinProgress({ serverId, command, fingerprint }: { serverId: string; command: string; fingerprint: string }) {
  // Watch for the agent's first heartbeat while the dialog is open.
  const servers = useListServers({ query: { refetchInterval: 3_000 } });
  const server = useMemo(() => (servers.data ?? []).find((x) => x.id === serverId), [servers.data, serverId]);
  const enrolled = server?.enrolled === true;
  const ready = enrolled && server?.status === "running";

  // §4 promises "running within 60s", so the page shows the clock it is being
  // judged against rather than an indefinite spinner. It stops when the server
  // is ready — a counter that keeps running past the outcome is just noise.
  const started = useRef(Date.now());
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => {
    // One tick unconditionally, so the reading is taken at the moment the
    // server goes ready rather than at the last whole second before it.
    const tick = () => setElapsed(Date.now() - started.current);
    tick();
    if (ready) return;
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [ready]);

  const steps = [
    { label: "join token issued · single use, expires shortly", state: "done" as const },
    { label: "agent dials home over mTLS", state: enrolled ? ("done" as const) : ("active" as const) },
    {
      label: "heartbeat · ready for workloads",
      state: ready ? ("done" as const) : enrolled ? ("active" as const) : ("pending" as const),
    },
  ];
  // Half credit for the step in flight — the bar tracks the list above it and
  // nothing else, so it stops where the work stops.
  const progress = ready ? 1 : enrolled ? 2.5 / 3 : 1.5 / 3;

  return (
    <div>
      {/* The command is a terminal line and looks like one wherever it is read
          (ui-principles §9: panes are ink in both themes). It wraps rather than
          truncates — a `curl | sh` you cannot read end to end is one you cannot
          check before running as root, and at 360px a single line shows almost
          nothing of it. */}
      <div className="rounded-md border border-pane-border bg-pane px-3.5 py-3">
        <div className="flex items-start gap-2.5">
          <span className="mt-px font-mono text-[11.5px] leading-[1.7] text-pane-ok" aria-hidden>
            $
          </span>
          <code className="min-w-0 flex-1 whitespace-pre-wrap break-all font-mono text-[11.5px] leading-[1.7] text-pane-text">
            {command}
          </code>
          <span className="-mr-1 -mt-1 shrink-0">
            <CopyButton value={command} label="Copy join command" />
          </span>
        </div>
      </div>

      {/* The fingerprint is what makes the pipe-to-shell defensible: it lets the
          operator confirm the CA the installer pins is this panel's. */}
      <div className="mt-2.5 flex items-start gap-2">
        <span className="shrink-0 font-mono text-[11px] leading-5 text-text-faint">ca fingerprint</span>
        <span className="min-w-0 flex-1 break-all font-mono text-[11px] leading-5 text-text-dim">{fingerprint}</span>
        <span className="-mt-0.5 shrink-0">
          <CopyButton value={fingerprint} label="Copy CA fingerprint" />
        </span>
      </div>

      <div className="mt-4 rounded-lg border border-border bg-surface px-4 py-3.5">
        <div className="flex items-baseline gap-3">
          <span className="text-[14px] font-bold text-text">
            {ready ? `${server?.name} is ready` : "Waiting for the agent…"}
          </span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-text-faint">{elapsedLabel(elapsed)}</span>
        </div>

        <ol className="mt-2.5 font-mono text-[11.5px] leading-[2.1] text-text-dim" aria-live="polite">
          {steps.map((s) => (
            <li key={s.label} className={cn(s.state === "pending" && "text-text-disabled")}>
              <span className={STEP_MARK[s.state].className} aria-hidden>
                {STEP_MARK[s.state].glyph}
              </span>{" "}
              {s.label}
            </li>
          ))}
        </ol>

        <div
          className="mt-2.5 h-[5px] overflow-hidden rounded-full bg-border-subtle"
          role="progressbar"
          aria-valuenow={Math.round(progress * 100)}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div
            className="h-full rounded-full bg-primary transition-[width] duration-500"
            style={{ width: `${progress * 100}%` }}
          />
        </div>

        {/* The enrollment runs on the control plane, not in this tab; saying so
            is what makes the ✕ safe to press. */}
        <p className="mt-2.5 text-[11px] leading-relaxed text-text-faint">
          {ready
            ? "Nothing else to do here — this server can take workloads now."
            : "Safe to close — the agent keeps joining, and the fleet row shows it arrive."}
        </p>
      </div>

      <div className="mt-4 flex flex-wrap items-center justify-end gap-2.5">
        {ready && (
          <Link
            to="/servers/$serverId"
            params={{ serverId }}
            className="mr-auto text-[12px] font-medium text-text-dim hover:underline"
          >
            Open {server?.name} →
          </Link>
        )}
        <DialogClose asChild>
          <Button variant={ready ? "primary" : "secondary"} size="lg">
            Done
          </Button>
        </DialogClose>
      </div>
    </div>
  );
}
