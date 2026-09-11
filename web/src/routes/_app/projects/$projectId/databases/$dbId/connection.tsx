// Database · Connection: how to reach it from inside the environment and from
// outside it, with a copy control on every value. The password is never here —
// it is shown once, at creation and at reset, and sealed after that
// (ui-principles §6).
import { createFileRoute, Link } from "@tanstack/react-router";
import { type ReactNode } from "react";
import { useGetDatabase, useGetDatabaseConnectionInfo } from "@/api/gen/databases/databases";
import { useGetServer } from "@/api/gen/servers/servers";
import type { ConnectionInfo, DatabaseEngine } from "@/api/gen/model";
import { CopyButton } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId/connection")({
  component: ConnectionTab,
});

/**
 * The port each engine listens on inside its container.
 *
 * `/connection-info` carries a single `port`, and publishing a host port
 * overwrites it with that one — so on an exposed database the internal row
 * would otherwise print the host's port and quietly fail to connect. The map
 * is the same one the create form publishes with, and matches the plane's own
 * defaults (core/api/rest/handlers_databases.go).
 */
const CONTAINER_PORT: Record<DatabaseEngine, number> = {
  postgresql: 5432,
  mysql: 3306,
  mariadb: 3306,
  mongodb: 27017,
  redis: 6379,
  valkey: 6379,
};

function ConnectionTab() {
  const { projectId, dbId } = Route.useParams();
  const info = useGetDatabaseConnectionInfo(dbId);
  // A cache read: the layout above already holds this record. It carries the
  // engine, which is what says how to read the port field.
  const db = useGetDatabase(dbId);
  const engine = db.data?.engine;

  return (
    <PageState query={info} isEmpty={() => false}>
      {(c) => (
        <div className="max-w-2xl space-y-7">
          <section>
            <Eyebrow>From apps in this environment</Eyebrow>
            <div className="mt-1.5">
              <InlineHint>
                Apps deployed alongside this database share a private network. This host resolves there and nowhere
                else, which is why nothing has to be exposed for an app to use it.
              </InlineHint>
            </div>
            <dl className="mt-3.5">
              <Row first label="Host" value={c.internal_host} />
              <Row label="Port" value={String(internalPort(c, engine))} />
              {c.user && <Row label="User" value={c.user} />}
              <Row label="Password">
                <span className="text-[12.5px] leading-relaxed text-text-dim">
                  Shown once when the database was created, then sealed. If it&rsquo;s lost,{" "}
                  <Link
                    to="/projects/$projectId/databases/$dbId"
                    params={{ projectId, dbId }}
                    className="text-accent hover:underline"
                  >
                    reset it on Overview
                  </Link>{" "}
                  — the replacement is shown once too.
                </span>
              </Row>
            </dl>
          </section>

          <section>
            <Eyebrow>From outside</Eyebrow>
            {c.host ? (
              <External host={c.host} port={c.port} />
            ) : (
              // No published host port means there is no external address to
              // copy. Say what is true and point at the connection that does
              // work, rather than offering an empty field or a remedy that
              // lands on a control the panel doesn't have (§11).
              <div className="mt-1.5">
                <InlineHint>
                  Nothing outside this environment can reach this database — no host port is published. External
                  access is chosen when a database is created; this one has it off, so apps use the internal host
                  above.
                </InlineHint>
              </div>
            )}
          </section>
        </div>
      )}
    </PageState>
  );
}

/** See CONTAINER_PORT: `port` means the published host port once there is one. */
function internalPort(c: ConnectionInfo, engine: DatabaseEngine | undefined): number {
  if (!c.host) return c.port;
  return (engine && CONTAINER_PORT[engine]) || c.port;
}

/**
 * The external half.
 *
 * `/connection-info` answers `host` with the ID of the server the database
 * runs on, not with an address — so this resolves the record and offers the
 * hostname the agent reports. It is offered as the server's address rather
 * than as the truth: an agent's hostname is whatever the machine calls itself,
 * which is not always what you reach it on from a laptop.
 */
function External({ host, port }: { host: string; port: number }) {
  const isServerId = host.startsWith("srv_");
  const server = useGetServer(host, { query: { enabled: isServerId, retry: false } });
  const address = isServerId ? (server.data?.hostname ?? "") : host;
  const name = server.data?.name;

  return (
    <>
      <div className="mt-1.5">
        <InlineHint>
          Published on {name ? <span className="mono text-[12px]">{name}</span> : "the server it runs on"} — anything
          that can reach that machine on this port can try to log in.
        </InlineHint>
      </div>
      <dl className="mt-3.5">
        {isServerId && server.isPending ? (
          // Hold the row rather than printing the fallback sentence for a
          // frame and then replacing it with an address (canvas 10e).
          <Row first label="Host">
            <span aria-hidden className="inline-block h-3 w-[120px] animate-pulse rounded bg-border-subtle" />
          </Row>
        ) : address ? (
          <Row first label="Host" value={address} />
        ) : (
          <Row first label="Host">
            <span className="text-[12.5px] leading-relaxed text-text-dim">
              The address you already reach {name ?? "this server"} on. CypherPanel publishes the port there; the
              name or IP is yours.
            </span>
          </Row>
        )}
        <Row label="Port" value={String(port)} />
      </dl>
    </>
  );
}

/**
 * One ruled row: mono label, the value, and its copy control. The list opens
 * on the 1.5px ink rule and separates on hairlines, the same way every other
 * list in the panel does — a value that is a sentence rather than a machine
 * string passes `children` and gets no copy button, because there is nothing
 * to paste.
 */
function Row({
  label,
  value,
  children,
  first,
}: {
  label: string;
  value?: string;
  children?: ReactNode;
  first?: boolean;
}) {
  return (
    <div
      className={cn(
        "flex gap-4 border-t py-2.5",
        first ? "border-t-[1.5px] border-border-strong" : "border-border",
        // A machine value and its copy button share a line; a sentence wraps,
        // and a label centred against three wrapped lines reads as a mistake.
        value !== undefined ? "items-center" : "items-start",
      )}
    >
      <dt className="eyebrow w-[74px] shrink-0">{label}</dt>
      <dd className="flex min-w-0 flex-1 items-center justify-between gap-2">
        {value !== undefined ? (
          <>
            <span className="mono truncate text-[13px] text-text" title={value}>
              {value}
            </span>
            <CopyButton value={value} label={`Copy ${label.toLowerCase()}`} />
          </>
        ) : (
          children
        )}
      </dd>
    </div>
  );
}
