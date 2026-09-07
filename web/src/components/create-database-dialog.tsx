// "+ Add resource" → new database (canvas 9b / 13z), and the whole lifecycle
// after the click (10a / 13ak): the pill goes busy and the form locks; a
// progress popup lists the real steps as the record moves; the result hands
// over the one plaintext copy of the password.
//
// Simple by default (ui-principles §6): pick an engine, name it, done. Server
// and version have working defaults; the application database folds into
// Advanced.
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plus, X } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { getListDatabasesQueryKey, useCreateDatabase, useGetDatabase } from "@/api/gen/databases/databases";
import { useListServers } from "@/api/gen/servers/servers";
import { DatabaseEngine } from "@/api/gen/model";
import { AdvancedSection } from "@/components/advanced-section";
import { radioArrowTarget } from "@/components/build-strategy-field";
import { CopyField } from "@/components/copy-field";
import { ProvisioningSteps, provisioningSteps } from "@/components/db-provisioning-steps";
import { JoinServerFirstDialog } from "@/components/join-server-first-dialog";
import { StatusDot } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { toastFailed } from "@/lib/toast";
import { cn } from "@/lib/utils";

/**
 * The engine tiles of canvas 9b: the engine is a card you click, with its
 * version as a small mono line inside the chosen one — not a name hidden in a
 * menu. The first version is the plane's own default (the engine matrix in
 * core/databases — what an omitted version would resolve to), so the tile
 * says what would happen anyway. `port` is what "expose externally" publishes
 * on the host; the engine's conventional port keeps that control a yes/no
 * instead of a number the operator has to invent.
 */
export const ENGINES: Record<DatabaseEngine, { label: string; versions: readonly string[]; port: number }> = {
  postgresql: { label: "Postgres", versions: ["16", "17", "15"], port: 5432 },
  mysql: { label: "MySQL", versions: ["8.4", "8.0"], port: 3306 },
  redis: { label: "Redis", versions: ["7.2"], port: 6379 },
  mongodb: { label: "Mongo", versions: ["7.0", "8.0"], port: 27017 },
  mariadb: { label: "MariaDB", versions: ["11", "10.11"], port: 3306 },
  valkey: { label: "Valkey", versions: ["8.0", "7.2"], port: 6379 },
};

/** 13z's row order: the four the canvas draws, then the two the API also
 *  offers. The full enum is kept — hiding an engine the plane supports would
 *  be a lie of omission. */
const ENGINE_ORDER: DatabaseEngine[] = [
  DatabaseEngine.postgresql,
  DatabaseEngine.mysql,
  DatabaseEngine.redis,
  DatabaseEngine.mongodb,
  DatabaseEngine.mariadb,
  DatabaseEngine.valkey,
];

/** "Postgres 16" — the tile's own words, for the board and the step list. */
export function engineLabel(engine: DatabaseEngine, version: string): string {
  return `${ENGINES[engine].label} ${version}`;
}

/** The 38×22 pill switch of 9b — the one toggle the create forms draw. */
function Toggle({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative h-[22px] w-[38px] shrink-0 rounded-full transition-colors",
        checked ? "bg-primary" : "bg-border-input",
      )}
    >
      <span
        className={cn(
          "absolute top-[2px] block h-[18px] w-[18px] rounded-full bg-surface transition-[left] motion-reduce:transition-none",
          checked ? "left-[18px]" : "left-[2px]",
        )}
      />
    </button>
  );
}

export function NewDatabaseDialog({
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
  const servers = useListServers();
  const [name, setName] = useState("");
  const [engine, setEngine] = useState<DatabaseEngine>(DatabaseEngine.postgresql);
  const [version, setVersion] = useState<string>(ENGINES.postgresql.versions[0] ?? "");
  const [serverId, setServerId] = useState("");
  const [initialDatabase, setInitialDatabase] = useState("");
  const [expose, setExpose] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Redis and Valkey number their databases rather than naming them, so there
  // is nothing to ask for.
  const namesDatabases = engine !== "redis" && engine !== "valkey";

  // A builder-only agent is built without a workload driver and rejects rollout
  // work, so offering it would create the resource and then fail every deploy.
  const enrolled = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const chosenServer = serverId || enrolled[0]?.id || "";
  const serverName = enrolled.find((s) => s.id === chosenServer)?.name ?? "the server";

  // The root password exists in plaintext exactly once, in the create
  // response. Navigating straight to the database threw it away, and nothing
  // can recover it afterwards — the stored copy is sealed. So the dialog stays
  // open and hands it over first (ui-principles §6: every generated value gets
  // a copy button).
  const [created, setCreated] = useState<{ id: string; name: string; password: string } | null>(null);

  const create = useCreateDatabase({
    mutation: {
      onSuccess: (res) => {
        // The board behind this popup is drawn from the cached list, so a
        // database created here is invisible there until the list is asked for
        // again — including the rollup chip, which counts the same two lists.
        void qc.invalidateQueries({ queryKey: getListDatabasesQueryKey(envId) });
        setCreated({ id: res.database.id, name: res.database.name, password: res.root_password });
      },
      // The pill offers the retry; the toast carries the why (10b/10c). The
      // inline line keeps the server's sentence beside the form it is about.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not create the database");
        toastFailed("Could not create the database", e, { retry: () => create.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(create);

  const trigger = (
    <Button variant="primary" size={primary ? "lg" : "md"}>
      <Plus className="h-3.5 w-3.5" /> New database
    </Button>
  );

  if (!servers.isPending && enrolled.length === 0) {
    return <JoinServerFirstDialog trigger={trigger} resource="database" />;
  }

  const choose = (key: DatabaseEngine) => {
    setEngine(key);
    setVersion(ENGINES[key].versions[0] ?? "");
  };

  const onTileKey = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const target = radioArrowTarget(e.key, index, ENGINE_ORDER.length);
    if (target === null) return;
    e.preventDefault();
    const next = ENGINE_ORDER[target];
    if (!next) return;
    choose(next);
    const tiles = e.currentTarget.closest('[role="radiogroup"]')?.querySelectorAll<HTMLButtonElement>('[role="radio"]');
    tiles?.[target]?.focus();
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      id: envId,
      data: {
        name,
        engine,
        version,
        server_id: chosenServer,
        ...(expose ? { expose_port: ENGINES[engine].port } : {}),
        ...(namesDatabases && initialDatabase.trim() !== ""
          ? { initial_database: initialDatabase.trim() }
          : {}),
      },
    });
  };

  return (
    <Dialog onOpenChange={(open) => !open && setCreated(null)}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      {created ? (
        <CreatePanel
          created={created}
          projectId={projectId}
          engineLabel={engineLabel(engine, version)}
          serverName={serverName}
        />
      ) : (
        <DialogContent
          size="form"
          // 13z: the last segment — where this dialog is acting — in accent.
          eyebrow={
            <>
              {projectName} / {envName} / <span className="text-accent">new database</span>
            </>
          }
          title="Create a database"
        >
          <form onSubmit={submit} className="space-y-4">
            {/* 10a locks the form while the create is in flight: a second
                submit would provision a second database. */}
            <fieldset disabled={create.isPending} className="min-w-0 space-y-4">
              {/* Six engines, and the widest labels ("Postgres", "MariaDB")
                  are single unbreakable words: at 360px four columns leave a
                  50px text box and the words spill into their neighbours. The
                  canvas draws this grid at the 560px dialog width only. */}
              <div
                role="radiogroup"
                aria-label="Engine"
                className="grid grid-cols-2 gap-2 min-[420px]:grid-cols-3 sm:grid-cols-4"
              >
                {ENGINE_ORDER.map((key, i) => {
                  const spec = ENGINES[key];
                  const checked = key === engine;
                  return (
                    <div
                      key={key}
                      className={cn(
                        "rounded-lg bg-surface text-center text-[12.5px] font-semibold transition-colors",
                        checked
                          ? "border-[1.5px] border-border-strong text-text"
                          : "border border-border text-text-mid hover:border-border-strong",
                      )}
                    >
                      {/* The tile is the radio; the version menu rides inside
                          the chosen one as its own control, since a select
                          cannot live inside a button. */}
                      <button
                        type="button"
                        role="radio"
                        aria-checked={checked}
                        tabIndex={checked ? 0 : -1}
                        onClick={() => choose(key)}
                        onKeyDown={(e) => onTileKey(e, i)}
                        className={cn(
                          "block w-full rounded-lg px-1.5 pt-3 focus-visible:outline-offset-[-3px]",
                          checked ? "pb-0" : "pb-3",
                        )}
                      >
                        {spec.label}
                        {!checked && (
                          <span className="mt-[3px] block font-mono text-[10.5px] font-normal text-text-faint">
                            {spec.versions[0]}
                          </span>
                        )}
                      </button>
                      {checked && (
                        // The version is a menu on the engine you chose, not a
                        // second question about an engine you might not want.
                        <Select
                          aria-label="Version"
                          value={version}
                          onChange={(e) => setVersion(e.target.value)}
                          className="mx-auto mb-3 mt-[3px] h-auto w-auto border-0 bg-transparent p-0 text-center text-[10.5px] font-normal text-text-faint"
                        >
                          {spec.versions.map((v) => (
                            <option key={v} value={v}>
                              {v}
                            </option>
                          ))}
                        </Select>
                      )}
                    </div>
                  );
                })}
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Name">
                  {(id) => (
                    <Input
                      id={id}
                      required
                      autoFocus
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="primary"
                    />
                  )}
                </Field>
                {/* Placement is always stated (9b) — a one-server fleet is
                    still an answer to "where does this land", in a field that
                    reads as a field. */}
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
              </div>

              {/* 9b asks this as a yes/no, and the sub-line names the
                  consequence rather than the port number — until it is on, at
                  which point the port is exactly what the operator needs. */}
              <div className="flex items-center gap-3 rounded-lg border border-border bg-surface px-3.5 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-semibold text-text">Expose externally</p>
                  <p className="mt-0.5 text-xs leading-relaxed text-text-mid">
                    {expose
                      ? `Publishes port ${ENGINES[engine].port} on ${serverName} — anything that can reach the server can reach the database.`
                      : "Off = reachable only from apps in this project (recommended)."}
                  </p>
                </div>
                <Toggle checked={expose} onChange={setExpose} label="Expose externally" />
              </div>

              {/* Everything with a working default folds in here (ui-principles
                  §6); Redis and Valkey have nothing to fold. */}
              {namesDatabases && (
                <AdvancedSection note="defaults work">
                  <Field
                    label="Application database"
                    hint="Optional — a database created inside the engine for your app to use. Cannot be changed later."
                  >
                    {(id) => (
                      <Input
                        id={id}
                        value={initialDatabase}
                        onChange={(e) => setInitialDatabase(e.target.value)}
                        placeholder={engine === "postgresql" ? "postgres" : "appdb"}
                        pattern="[A-Za-z_][A-Za-z0-9_]*"
                        maxLength={63}
                        title="Letters, digits and underscores; must not start with a digit."
                      />
                    )}
                  </Field>
                </AdvancedSection>
              )}
            </fieldset>

            {error && (
              <p role="alert" className="text-[13px] text-danger">
                {error}
              </p>
            )}
            {/* 13z footer: the accent pill and the note — esc and ✕ close. */}
            <div className="flex flex-wrap items-center gap-2.5 pt-1">
              <ActionButton type="submit" variant="accent" size="lg" state={state} busyLabel="Creating…">
                Create database →
              </ActionButton>
              <span className="text-xs text-text-faint">password generated and shown once, like tokens</span>
            </div>
          </form>
        </DialogContent>
      )}
    </Dialog>
  );
}

/** 10a prints the create's own elapsed time — "8.4s" — beside the result. */
function elapsedLabel(ms: number): string {
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m ${String(Math.round(s % 60)).padStart(2, "0")}s`;
}

/**
 * Canvas 10a, panels 2 and 3 — what the click actually looks like.
 *
 * The record rides the /events stream: every status transition the agent
 * reports invalidates this query (lib/live.tsx), and the query client's own
 * 5s poll covers the stream being down. No timer here pretends to know how
 * far along the agent is.
 *
 * The secret is handed over in both panels rather than only in the last one:
 * 10a promises the popup is safe to close, and that promise is only honest if
 * closing it early does not take the one plaintext copy with it. (The canvas
 * draws panel 2 without it; the code keeps it on purpose.)
 */
function CreatePanel({
  created,
  projectId,
  engineLabel: engine,
  serverName,
}: {
  created: { id: string; name: string; password: string };
  projectId: string;
  engineLabel: string;
  serverName: string;
}) {
  const navigate = useNavigate();
  const db = useGetDatabase(created.id);
  const { steps, progress, ready, failed } = provisioningSteps(db.data?.status ?? "provisioning", engine, serverName);

  // Frozen the moment it came up: a clock still ticking behind a finished
  // result reads as work still happening.
  const startedAt = useRef(Date.now());
  const [tookMs, setTookMs] = useState<number | null>(null);
  useEffect(() => {
    if (ready) setTookMs((t) => t ?? Date.now() - startedAt.current);
  }, [ready]);

  const openDatabase = () =>
    void navigate({
      to: "/projects/$projectId/databases/$dbId",
      params: { projectId, dbId: created.id },
    });

  const secret = (
    <>
      <p className="mt-2 text-xs leading-relaxed text-text-mid">Copy the password now — it's sealed after this popup.</p>
      {/* The one field in the product set on ink: 10a puts the secret on the
          pane so it reads as something handed over, not something stored. */}
      <CopyField
        value={created.password}
        className="mt-2.5 border-transparent bg-toast px-3.5 py-2.5 text-toast-text [&_button:hover]:bg-pane-border [&_button:hover]:text-toast-text"
      />
    </>
  );

  if (ready) {
    return (
      <DialogContent
        title={`${created.name} is running`}
        hideTitle
        className="border-[1.5px] border-border-strong bg-surface [&>div:first-child]:p-0"
      >
        <div className="pt-6">
          <div className="flex items-center gap-2">
            <StatusDot status="running" />
            <span className="text-[14px] font-bold text-text">{created.name} is running</span>
            {tookMs !== null && (
              <span className="ml-auto font-mono text-[10.5px] text-text-faint">{elapsedLabel(tookMs)}</span>
            )}
          </div>
          {secret}
          <div className="mt-3.5 flex flex-wrap gap-2">
            <Button variant="primary" size="sm" onClick={openDatabase}>
              Open {created.name}
            </Button>
            <DialogClose asChild>
              <Button variant="secondary" size="sm">
                Done
              </Button>
            </DialogClose>
          </div>
        </div>
      </DialogContent>
    );
  }

  const title = failed ? `${created.name} could not start` : `Creating ${created.name}…`;

  return (
    <DialogContent title={title} hideTitle className="border border-border bg-surface [&>div:first-child]:p-0">
      <div className="pt-6">
        <div className="flex items-baseline gap-3">
          <span className="text-[14px] font-bold text-text">{title}</span>
          {/* The footnote promises this popup can be closed, so there has to be
              something to click that closes it. */}
          <DialogClose aria-label="Close" className="ml-auto shrink-0 rounded p-0.5 text-text-faint hover:text-text">
            <X className="h-4 w-4" />
          </DialogClose>
        </div>

        <ProvisioningSteps
          steps={steps}
          progress={progress}
          failed={failed}
          detail={db.data?.status_detail}
          label={title}
          className="mt-2.5"
        />

        {secret}

        {failed ? (
          <div className="mt-3.5 flex flex-wrap items-center gap-2.5">
            <Button variant="primary" size="sm" onClick={openDatabase}>
              Open {created.name}
            </Button>
            <span className="text-[11px] text-text-faint">
              the record is on the project board — retry or remove it from there
            </span>
          </div>
        ) : (
          <p className="mt-2.5 text-[11px] leading-relaxed text-text-faint">
            safe to close — creation continues; the card on the project board shows progress
          </p>
        )}
      </div>
    </DialogContent>
  );
}
