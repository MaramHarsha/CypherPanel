// Database · Connection: copy-ready connection fields, internal vs external
// host. The password is never shown here — only a hint (ui-principles §6).
import { createFileRoute } from "@tanstack/react-router";
import { useGetDatabaseConnectionInfo } from "@/api/gen/databases/databases";
import { CopyField } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId/connection")({
  component: ConnectionTab,
});

function ConnectionTab() {
  const { dbId } = Route.useParams();
  const info = useGetDatabaseConnectionInfo(dbId);

  return (
    <PageState query={info} isEmpty={() => false}>
      {(c) => (
        <div className="max-w-xl space-y-6">
          <section className="space-y-2">
            <Eyebrow>Inside CypherPanel</Eyebrow>
            <InlineHint>Use this host from apps in the same project — traffic stays on the internal network.</InlineHint>
            <Pair label="Host" value={c.internal_host} />
            <Pair label="Port" value={String(c.port)} />
            {c.user && <Pair label="User" value={c.user} />}
            <Pair label="Password" value={c.password_hint} hintOnly />
          </section>

          <section className="space-y-2">
            <Eyebrow>From outside</Eyebrow>
            <InlineHint>Reachable externally only if you exposed a port when creating the database.</InlineHint>
            <Pair label="Host" value={c.host} />
            <Pair label="Port" value={String(c.port)} />
          </section>
        </div>
      )}
    </PageState>
  );
}

function Pair({ label, value, hintOnly }: { label: string; value: string; hintOnly?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="eyebrow shrink-0">{label}</span>
      {hintOnly ? (
        <span className="mono text-[13px] text-text-faint">{value}</span>
      ) : (
        <CopyField value={value} className="max-w-xs flex-1" />
      )}
    </div>
  );
}
