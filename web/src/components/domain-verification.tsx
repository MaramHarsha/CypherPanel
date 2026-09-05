// The verification state of an application's domain (dns-automation.md §6,
// design canvas 17b).
//
// It sits directly under the Domain field because it is about *that* value.
// Status uses the product's existing shape language (ui-principles §5): round
// for a settled good state, square for one that needs a person.
//
// When no DNS provider is connected this renders NOTHING. An install that never
// connects Cloudflare should not grow a row of unexplained "unverified" labels —
// the feature is opt-in, and so is its UI.
import { Link } from "@tanstack/react-router";
import { type ReactNode } from "react";
import { useGetApplication, useGetApplicationDNS } from "@/api/gen/applications/applications";
import { useGetServer } from "@/api/gen/servers/servers";
import { StatusDot } from "@/components/status-badge";
import { cn } from "@/lib/utils";

export function DomainVerification({ applicationId }: { applicationId: string }) {
  const { data } = useGetApplicationDNS(applicationId);

  // The one state that points somewhere else — "set it on the server" — needs
  // the server's id and name. ApplicationDNS carries neither and its `reason`
  // is prose, but the application (already cached by any page that shows this)
  // knows its server, and the server's own query supplies the name.
  const noAddress = Boolean(data?.reason && /no public address/i.test(data.reason));
  const app = useGetApplication(applicationId, { query: { enabled: noAddress } });
  const serverId = app.data?.runtime.server_id ?? "";
  const server = useGetServer(serverId, { query: { enabled: noAddress && serverId !== "", retry: false } });

  if (!data || !data.enforced) return null;

  // Nothing claimed, nothing to verify.
  if (!data.domain) return null;

  if (!data.verified) {
    return (
      <Note tone="pending">
        Verification pending in Cloudflare — <s className="font-mono text-[11.5px]">{data.domain}</s> is outside your
        zones
        {data.available_zones.length > 0 ? (
          <>
            {" "}
            (<Mono>{data.available_zones.join(", ")}</Mono>)
          </>
        ) : (
          " — none are visible to the token"
        )}{" "}
        · not routed, no cert requested · <DNSLink />
      </Note>
    );
  }

  if (data.last_error) {
    // The panel never overwrites a record it did not create, so a record that
    // already exists with other content is a conflict only a person can settle.
    const conflict = /already exists/i.test(data.last_error);
    return (
      <Note tone="error">
        Record error — Cloudflare: <Mono>&ldquo;{data.last_error}&rdquo;</Mono>
        {conflict && " · conflict is yours to resolve"}
        {data.next_attempt_at && <> · retrying {retryIn(data.next_attempt_at)}</>}
      </Note>
    );
  }

  if (data.reason) {
    if (noAddress) {
      return (
        <Note tone="pending">
          This server has no public address — set it on{" "}
          {serverId ? (
            <Link
              to="/servers/$serverId"
              params={{ serverId }}
              className="font-semibold text-text underline-offset-2 hover:underline"
            >
              {server.data?.name ?? "the server"}
            </Link>
          ) : (
            "the server"
          )}{" "}
          so the A record has somewhere to point
        </Note>
      );
    }
    return (
      <Note tone="pending">
        Verified · zone <Mono>{data.zone}</Mono> · no record yet — {data.reason}
      </Note>
    );
  }

  if (!data.record_created) {
    return (
      <Note tone="pending">
        Verified · zone <Mono>{data.zone}</Mono> · record being created
      </Note>
    );
  }

  // Owning the zone and the domain resolving are different facts. Saying only
  // "verified" here would leave someone waiting for a domain that cannot work
  // until they finish the nameserver step at their registrar.
  if (data.zone_status && data.zone_status !== "active") {
    return (
      <Note tone="pending">
        Record created, but the zone is {data.zone_status} — point <Mono>{data.zone}</Mono>&rsquo;s nameservers at
        Cloudflare; it won&rsquo;t resolve until then
      </Note>
    );
  }

  return (
    <Note tone="ok">
      Verified · zone <Mono>{data.zone}</Mono> · record live at <Mono>{data.record_content}</Mono> · <DNSLink />
    </Note>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-[11.5px]">{children}</span>;
}

/** The way back to 17a — the zones, the token, the refresh. */
function DNSLink() {
  return (
    <Link to="/settings/dns" className="font-semibold underline-offset-2 hover:underline">
      DNS ↗
    </Link>
  );
}

/**
 * "in 4 min" — the canvas's phrasing for the next attempt. lib/time's
 * `timeUntil` says "in 4m", which reads as a fault code inside a sentence.
 */
function retryIn(iso: string): string {
  const s = Math.round((Date.parse(iso) - Date.now()) / 1000);
  if (Number.isNaN(s)) return "soon";
  if (s <= 0) return "now";
  if (s < 60) return `in ${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `in ${m} min`;
  return `in ${Math.round(m / 60)} h`;
}

// The verified line is green; the states that need a person keep body ink
// and let the marker carry the colour (canvas 17b) — amber in a sentence
// reads as mud, and the red square already says "error".
const TONES = {
  ok: { dot: "running", text: "text-status-running" },
  pending: { dot: "degraded", text: "text-text-dim" },
  error: { dot: "error", text: "text-text-dim" },
} as const;

function Note({ tone, children }: { tone: keyof typeof TONES; children: ReactNode }) {
  const t = TONES[tone];
  return (
    // Polite: the sentence changes when a record lands or a retry fires, and
    // a screen reader should hear the new verdict without being cut off.
    <p className={cn("mt-2 flex items-start gap-2 text-[12px] leading-[1.5]", t.text)} aria-live="polite">
      <StatusDot status={t.dot} className="mt-[5px] h-2 w-2" />
      <span className="min-w-0">{children}</span>
    </p>
  );
}
