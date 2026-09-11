// An application's domain, shown honestly (dns-automation.md §6).
//
// A bare hyperlink is a claim that clicking it reaches the app. For a domain
// that is not verified in Cloudflare that claim is false — the panel does not
// publish a route for it, so the link goes nowhere. Rendering it anyway is how
// someone ends up staring at a "running" application wondering why their domain
// is dead.
//
// So the link only appears when the panel will actually serve that hostname.
// Otherwise the same text appears unlinked, marked, and saying why.
import { ExternalLink } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useGetApplicationDNS } from "@/api/gen/applications/applications";

/**
 * The panel is serving this route over plain HTTP because NO CERTIFICATE ISSUER
 * is configured panel-wide (routing-and-tls.md §7). The API has reported it as
 * `tls_state` since agent-identity-and-tls.md §5 and nothing rendered it, so an
 * operator who ticked "HTTPS" got an `https://` link that could not work and no
 * hint as to why — which is the same class of dishonest link the unverified-DNS
 * branch below already refuses to draw.
 */
const NO_RESOLVER = "http_only_no_resolver";

/** The scheme the panel will actually answer on, whatever the route asked for. */
function servedScheme(https: boolean, tlsState?: string): "http" | "https" {
  return https && tlsState !== NO_RESOLVER ? "https" : "http";
}

export function DomainLink({
  applicationId,
  domain,
  https,
  tlsState,
}: {
  applicationId: string;
  domain: string;
  https: boolean;
  tlsState?: string;
}) {
  const { data } = useGetApplicationDNS(applicationId);
  if (!domain) return <>internal only</>;

  // Enforcement off (no DNS provider) means the panel routes every domain, as
  // it always did — so the link is truthful.
  const blocked = data?.enforced === true && data.verified === false;

  // The domain is verified and the panel STILL created no record. The API has
  // always said why (`reason`) and nothing rendered it, so the most common
  // cause — the server has no public address, which silently disables DNS
  // automation for every application on it — was invisible: the operator sees
  // a verified domain and assumes the record exists.
  const unmanaged = data?.enforced === true && data.verified === true && !data.record_created && !!data.reason;

  if (!blocked) {
    return (
      <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <a
          href={`${servedScheme(https, tlsState)}://${domain}`}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-accent hover:underline"
        >
          {domain} <ExternalLink className="h-3 w-3" aria-hidden />
        </a>
        {https && tlsState === NO_RESOLVER && (
          // Named, with the remedy, rather than a bare "insecure" badge: the
          // fix is one panel-wide setting and the operator cannot guess it.
          <span className="text-[11.5px] text-status-degraded-text">
            served over HTTP —{" "}
            <Link to="/settings/tls" className="underline hover:text-accent">
              set a certificate issuer
            </Link>{" "}
            to get HTTPS
          </span>
        )}
        {unmanaged && (
          <span className="flex items-center gap-1.5 text-[11.5px] text-status-degraded-text">
            <span className="size-[6px] shrink-0 bg-status-degraded" aria-hidden />
            no DNS record — {data?.reason}
          </span>
        )}
      </span>
    );
  }

  return (
    <span className="inline-flex items-center gap-1.5">
      {/* Square mark: this needs a person (ui-principles §5). */}
      <span className="size-[6px] shrink-0 bg-status-degraded" aria-hidden />
      <span className="mono text-text-mid line-through decoration-text-faint/60">{domain}</span>
      <span className="text-[11px] text-status-degraded-text">not verified in Cloudflare</span>
    </span>
  );
}

// HeaderDomain is the masthead's quieter variant: the same honesty, in the
// muted type the title row uses (the accent belongs to the Deploy pill).
export function HeaderDomain({
  applicationId,
  domain,
  https,
  tlsState,
}: {
  applicationId: string;
  domain: string;
  https: boolean;
  tlsState?: string;
}) {
  const { data } = useGetApplicationDNS(applicationId);
  if (!domain) return null;

  if (data?.enforced === true && data.verified === false) {
    return (
      <span className="inline-flex items-center gap-1.5 font-mono text-[12px] text-status-degraded-text">
        <span className="size-[6px] shrink-0 bg-status-degraded" aria-hidden />
        {domain} · unverified
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5">
      <a
        href={`${servedScheme(https, tlsState)}://${domain}`}
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-1 font-mono text-[12px] text-text-mid hover:text-text"
      >
        {domain} <ExternalLink className="h-3 w-3" aria-hidden />
      </a>
      {https && tlsState === NO_RESOLVER && (
        <Link
          to="/settings/tls"
          title="No certificate issuer is configured, so this route is served over plain HTTP"
          className="font-mono text-[11px] text-status-degraded-text hover:underline"
        >
          http only
        </Link>
      )}
    </span>
  );
}
