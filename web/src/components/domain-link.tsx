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
import { useGetApplicationDNS } from "@/api/gen/applications/applications";

export function DomainLink({
  applicationId,
  domain,
  https,
}: {
  applicationId: string;
  domain: string;
  https: boolean;
}) {
  const { data } = useGetApplicationDNS(applicationId);
  if (!domain) return <>internal only</>;

  // Enforcement off (no DNS provider) means the panel routes every domain, as
  // it always did — so the link is truthful.
  const blocked = data?.enforced === true && data.verified === false;

  if (!blocked) {
    return (
      <a
        href={`${https ? "https" : "http"}://${domain}`}
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-1 text-accent hover:underline"
      >
        {domain} <ExternalLink className="h-3 w-3" aria-hidden />
      </a>
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
}: {
  applicationId: string;
  domain: string;
  https: boolean;
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
    <a
      href={`${https ? "https" : "http"}://${domain}`}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1 font-mono text-[12px] text-text-mid hover:text-text"
    >
      {domain} <ExternalLink className="h-3 w-3" aria-hidden />
    </a>
  );
}
