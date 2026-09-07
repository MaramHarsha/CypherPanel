// Annotating a live incident (status-pages.md).
//
// The spec is unusually specific about who this is for: *"the person who
// notices at 02:00 is on call, not an admin; making them find one before they
// can write 'we are aware and investigating' is how a status page stops being
// used."* Member rank, deliberately — and until now there was no control at
// all, so the only way to put a sentence on a public incident was the API.
//
// It sits beside the preview because that is where the incident is visible: you
// read what a visitor reads, and write the line into the thing you are looking
// at. A separate incidents screen would be a second place to look during the
// one moment nobody has time to look twice.
//
// The text is never linkified — the server escapes it and renders it as text,
// so a public hostname a customer trusts cannot become a redirect.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { PublicStatusIncident } from "@/api/gen/model";
import {
  getPreviewStatusPageQueryKey,
  useAnnotateStatusIncident,
} from "@/api/gen/projects/projects";
import { ActionButton } from "@/components/ui/action-button";
import { Eyebrow } from "@/components/eyebrow";
import { Input } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

/** The server's own ceiling, so the field stops where the API would refuse. */
const MAX = 280;

export function IncidentAnnotations({
  pageId,
  incidents,
}: {
  pageId: string;
  incidents: PublicStatusIncident[];
}) {
  if (incidents.length === 0) return null;
  return (
    <section className="space-y-2.5">
      <Eyebrow>Incidents</Eyebrow>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        One line each, shown to anyone reading the page. Plain text — it is never turned into a link.
      </p>
      <div className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
        {incidents.map((i) => (
          <IncidentRow key={i.id} pageId={pageId} incident={i} />
        ))}
      </div>
    </section>
  );
}

function IncidentRow({ pageId, incident }: { pageId: string; incident: PublicStatusIncident }) {
  const qc = useQueryClient();
  const [message, setMessage] = useState(incident.message ?? "");
  const save = useAnnotateStatusIncident({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getPreviewStatusPageQueryKey(pageId) });
        toastSuccess(message.trim() === "" ? "Note cleared" : "Note published");
      },
      onError: (e: unknown) => toastFailed("Could not publish the note", e),
    },
  });

  const live = incident.ended_at == null;
  const dirty = message !== (incident.message ?? "");

  return (
    <div className="space-y-2 px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        {/* A live incident is the one someone is looking for at 02:00, so it
            leads with the marker rather than the timestamp. */}
        {live && <span className="size-[6px] shrink-0 rounded-full bg-status-error" aria-hidden />}
        <span className="text-[13px] font-medium text-text">{incident.component}</span>
        <span className="mono text-[11.5px] text-text-faint">
          {live ? "ongoing" : "resolved"} · {incident.duration}
        </span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <span className="min-w-[220px] flex-1">
          <Input
            value={message}
            maxLength={MAX}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="We are aware and investigating."
            aria-label={`Note for the ${incident.component} incident`}
          />
        </span>
        <ActionButton
          variant="secondary"
          size="sm"
          state={save.isPending ? "busy" : "idle"}
          busyLabel="Publishing…"
          disabledReason={!dirty ? "Nothing has changed" : undefined}
          onClick={() => save.mutate({ id: pageId, iid: incident.id, data: { message: message.trim() } })}
        >
          {message.trim() === "" && incident.message ? "Clear" : "Publish"}
        </ActionButton>
      </div>
      <p className="text-[11.5px] text-text-faint">
        {MAX - message.length} characters left · visible to everyone who opens the page
      </p>
    </div>
  );
}
