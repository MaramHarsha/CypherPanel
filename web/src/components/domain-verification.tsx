// The verification state of an application's domain (dns-automation.md §6).
//
// It sits directly under the Domain field because it is about *that* value.
// Status uses the product's existing shape language (ui-principles §5): round
// for a settled good state, square for one that needs a person.
//
// When no DNS provider is connected this renders NOTHING. An install that never
// connects Cloudflare should not grow a row of unexplained "unverified" labels —
// the feature is opt-in, and so is its UI.
import { useGetApplicationDNS } from "@/api/gen/applications/applications";
import { Link } from "@tanstack/react-router";
import { relativeTime } from "@/lib/time";

export function DomainVerification({ applicationId }: { applicationId: string }) {
  const { data } = useGetApplicationDNS(applicationId);
  if (!data || !data.enforced) return null;

  // Nothing claimed, nothing to verify.
  if (!data.domain) return null;

  if (!data.verified) {
    return (
      <Note tone="pending">
        <strong className="font-semibold">Verification pending in Cloudflare.</strong>{" "}
        This domain is not inside any zone the panel can manage, so it will not be routed.{" "}
        {data.available_zones.length > 0 ? (
          <>
            Connected zones: <span className="mono">{data.available_zones.join(", ")}</span>. Add this domain to
            Cloudflare, then <Link to="/settings/dns" className="underline">refresh the zones</Link>.
          </>
        ) : (
          <>
            No zones are visible to the token —{" "}
            <Link to="/settings/dns" className="underline">check the DNS settings</Link>.
          </>
        )}
      </Note>
    );
  }

  if (data.last_error) {
    return (
      <Note tone="error">
        <strong className="font-semibold">Cloudflare refused the record.</strong>{" "}
        <span className="mono">{data.last_error}</span>
        {data.next_attempt_at && <> Retrying {relativeTime(data.next_attempt_at)}.</>}
      </Note>
    );
  }

  if (data.reason) {
    return (
      <Note tone="pending">
        <strong className="font-semibold">Verified, but no record yet.</strong> {data.reason}
      </Note>
    );
  }

  if (!data.record_created) {
    return (
      <Note tone="pending">
        Verified in <span className="mono">{data.zone}</span> — the DNS record is being created.
      </Note>
    );
  }

  // Owning the zone and the domain resolving are different facts. Saying only
  // "verified" here would leave someone waiting for a domain that cannot work
  // until they finish the nameserver step at their registrar.
  if (data.zone_status && data.zone_status !== "active") {
    return (
      <Note tone="pending">
        <strong className="font-semibold">Record created, but the zone is {data.zone_status}.</strong>{" "}
        <span className="mono">{data.record_name}</span> points at <span className="mono">{data.record_content}</span>{" "}
        in Cloudflare. It will not resolve until you point your registrar's nameservers at Cloudflare.
      </Note>
    );
  }

  return (
    <Note tone="ok">
      Verified in <span className="mono">{data.zone}</span>.{" "}
      <span className="mono">{data.record_name}</span> points at <span className="mono">{data.record_content}</span>,
      managed by CypherPanel.
    </Note>
  );
}

const TONES = {
  ok: { dot: "bg-status-running rounded-full", text: "text-text-mid" },
  pending: { dot: "bg-status-degraded rounded-full", text: "text-status-degraded-text" },
  error: { dot: "bg-status-error", text: "text-danger" },
} as const;

function Note({ tone, children }: { tone: keyof typeof TONES; children: React.ReactNode }) {
  const t = TONES[tone];
  return (
    <p className={`mt-1.5 flex items-start gap-1.5 text-[11.5px] leading-relaxed ${t.text}`}>
      <span className={`mt-[0.42rem] size-[6px] shrink-0 ${t.dot}`} aria-hidden />
      <span>{children}</span>
    </p>
  );
}
