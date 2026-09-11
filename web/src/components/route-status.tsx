// RouteStatus — design canvas 13c (dark twin of 6c): "cert as a route
// condition". One row per routed hostname: a dot, the hostname in mono, one
// sentence about how it is served, and a mono chip at the right.
//
// What the row may claim is bounded by what the plane can see. It knows the
// route's configuration (domain, https, path prefix), what the DNS provider
// reports when one is connected (ApplicationDNS), and — on demand — whether
// the hostname actually reaches this app (DomainCheck). It does NOT know
// whether Let's Encrypt has issued the certificate: that lives in the serving
// node's acme.json and routing-and-tls.md §7/§10 defers reporting it. So the
// canvas's "CERT ✓" chip is not drawn here; the happy row says what is true
// (HTTPS is requested and the resolver renews on the node) and the dot stays
// hollow until something has actually been observed (ui-principles §5, §10 —
// never fake certainty).
import { Link } from "@tanstack/react-router";
import { type ReactNode } from "react";
import { useCheckApplicationDomain, useGetApplicationDNS } from "@/api/gen/applications/applications";
import type { Application, ApplicationDNS, DomainCheck } from "@/api/gen/model";
import { useGetServer } from "@/api/gen/servers/servers";
import { StatusDot } from "@/components/status-badge";
import { ActionButton } from "@/components/ui/action-button";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

type Tone = "ok" | "attention" | "error" | "unknown";

interface Condition {
  tone: Tone;
  /** Mono uppercase chip at the row's right edge. */
  chip: string;
  /** The sentence under the hostname. */
  detail: ReactNode;
  /** Mono evidence line — what was observed, when there is something. */
  evidence?: string;
}

const TONE_TEXT: Record<Tone, string> = {
  ok: "text-text-faint",
  attention: "text-status-degraded-text",
  error: "text-danger",
  unknown: "text-text-faint",
};

const TONE_CHIP: Record<Tone, string> = {
  ok: "text-status-running",
  attention: "text-status-degraded-text",
  error: "text-danger",
  unknown: "text-text-faint",
};

const TONE_ROW: Record<Tone, string | undefined> = {
  ok: undefined,
  attention: "bg-status-degraded/[0.05]",
  error: "bg-status-error/[0.05]",
  unknown: undefined,
};

/** The dot vocabulary is the status one (ui-principles §5): round for ok and
 *  amber, square for error, hollow for not-yet-observed. */
const TONE_DOT: Record<Tone, string> = {
  ok: "running",
  attention: "degraded",
  error: "error",
  unknown: "unknown",
};

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono">{children}</span>;
}

/**
 * What the row says, decided in order of how much a person has to do about it.
 * A DNS provider's refusal outranks a missing record, which outranks a check
 * that found nothing answering, which outranks the plain configuration.
 */
function describe({
  https,
  dns,
  check,
  address,
  serverId,
}: {
  https: boolean;
  dns: ApplicationDNS | undefined;
  check: DomainCheck | undefined;
  address: string | undefined;
  serverId: string;
}): Condition {
  const managed = dns?.enforced === true;

  if (managed && dns.last_error) {
    return {
      tone: "error",
      chip: "DNS ERROR",
      detail: (
        <>
          Cloudflare refused the record — <Mono>{dns.last_error}</Mono>
          {dns.next_attempt_at && <>. Retrying {relativeTime(dns.next_attempt_at)}</>}.
        </>
      ),
    };
  }

  if (managed && !dns.verified) {
    return {
      tone: "attention",
      chip: "UNVERIFIED",
      detail: (
        <>
          Not verified in Cloudflare — outside every connected zone
          {dns.available_zones.length > 0 && (
            <>
              {" "}
              (<Mono>{dns.available_zones.join(", ")}</Mono>)
            </>
          )}
          , so the panel won&rsquo;t publish this route. Add it to Cloudflare, then{" "}
          <Link to="/settings/dns" className="underline underline-offset-2">
            refresh the zones
          </Link>
          .
        </>
      ),
    };
  }

  if (managed && dns.reason) {
    return { tone: "attention", chip: "NO RECORD", detail: <>Verified, but no record yet — {dns.reason}</> };
  }

  if (managed && !dns.record_created) {
    return {
      tone: "attention",
      chip: "DNS PENDING",
      detail: (
        <>
          Verified in <Mono>{dns.zone}</Mono> — the DNS record is being created.
        </>
      ),
    };
  }

  if (managed && dns.zone_status && dns.zone_status !== "active") {
    return {
      tone: "attention",
      chip: "DNS PENDING",
      detail: (
        <>
          Record created, but the zone is {dns.zone_status} — <Mono>{dns.record_name}</Mono> points at{" "}
          <Mono>{dns.record_content}</Mono> in Cloudflare. It won&rsquo;t resolve until your registrar&rsquo;s
          nameservers point at Cloudflare.
        </>
      ),
    };
  }

  if (check?.verdict === "no_dns") {
    // The canvas names the address to point at rather than "your server's
    // IP": the plane already stores it (Server.public_address), so say it.
    // What it must not say is "serving over HTTP meanwhile" — the proxy
    // redirects to https whenever the route asks for it, issued or not.
    return {
      tone: "attention",
      chip: "NO DNS",
      detail: (
        <>
          DNS doesn&rsquo;t point here yet
          {https ? " — Let's Encrypt can't issue a certificate until it does" : " — nothing reaches the app at this name"}
          ; the deploy itself is unaffected.{" "}
          {address ? (
            <>
              Point an A record at <Mono>{address}</Mono>, then check again.
            </>
          ) : (
            <>
              Point an A record at this server&rsquo;s public address —{" "}
              <Link to="/servers/$serverId" params={{ serverId }} className="underline underline-offset-2">
                set it on the server
              </Link>{" "}
              so the panel can name it here.
            </>
          )}
        </>
      ),
    };
  }

  if (check && check.verdict !== "ok" && check.verdict !== "no_domain") {
    return {
      tone: "attention",
      chip: check.verdict === "unreachable" ? "UNREACHABLE" : "SERVED ELSEWHERE",
      detail: (
        <>
          {check.summary}
          {check.remedy && <> {check.remedy}</>}
        </>
      ),
      evidence: evidenceOf(check),
    };
  }

  // The plain configuration — HTTPS requested (or not), and whether anything
  // has confirmed the hostname reaches this app.
  const observed =
    check?.verdict === "ok" || (managed && dns.verified && dns.record_created && (!dns.zone_status || dns.zone_status === "active"));
  const evidence =
    check?.verdict === "ok"
      ? evidenceOf(check)
      : managed && dns.record_name && dns.record_content
        ? `${dns.record_name} → ${dns.record_content} · managed by CypherPanel`
        : undefined;

  if (!https) {
    return {
      tone: observed ? "ok" : "unknown",
      chip: "HTTP ONLY",
      detail: "HTTP · no certificate is requested for this route, so it is served in the clear.",
      evidence,
    };
  }
  return {
    tone: observed ? "ok" : "unknown",
    chip: "HTTPS",
    detail: "HTTPS · Let's Encrypt · renews itself on the serving node",
    evidence,
  };
}

function evidenceOf(r: DomainCheck): string | undefined {
  const parts = [
    r.resolved_ips.length > 0 ? `resolves to ${r.resolved_ips.join(", ")}` : null,
    r.http_status ? `answered ${r.http_status}` : null,
    r.served_by ? `served by ${r.served_by}` : null,
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

/**
 * The route list for one application. `app` is the SAVED application — the
 * row reports the hostname the plane has, not one still being typed in the
 * field above it, because every fact on the row is fetched by application id.
 */
export function RouteStatus({ app, className }: { app: Application; className?: string }) {
  const domain = app.route.domain ?? "";
  return (
    <div className={cn("overflow-hidden rounded-lg border border-border bg-surface", className)}>
      {domain ? (
        <RouteRow app={app} domain={domain} />
      ) : (
        <p className="px-4 py-3 text-[12.5px] leading-relaxed text-text-faint">
          No route yet — the app is reachable only from inside its environment. Add a domain above to publish it.
        </p>
      )}
    </div>
  );
}

function RouteRow({ app, domain }: { app: Application; domain: string }) {
  const dns = useGetApplicationDNS(app.id);
  // On demand, never polled: the check makes an outbound request to the public
  // internet. It shares its cache with the Overview's check, so a verdict
  // found there is the verdict shown here.
  const check = useCheckApplicationDomain(app.id, { query: { enabled: false, retry: false } });
  const server = useGetServer(app.runtime.server_id, { query: { retry: false } });

  const c = describe({
    https: app.route.https,
    dns: dns.data,
    check: check.data,
    address: server.data?.public_address,
    serverId: app.runtime.server_id,
  });

  return (
    <div className={cn("flex items-center gap-3 px-4 py-3", TONE_ROW[c.tone])}>
      <StatusDot status={TONE_DOT[c.tone]} className="h-2 w-2" />
      <div className="min-w-0 flex-1">
        <span className="font-mono text-[12.5px] font-medium text-text">{domain}</span>
        {app.route.path_prefix && app.route.path_prefix !== "/" && (
          <span className="font-mono text-[12.5px] text-text-mid">{app.route.path_prefix}</span>
        )}
        {/* Polite: the sentence changes when a check lands, and a screen reader
            should hear the new verdict without being cut off (canvas 14g). */}
        <div className={cn("mt-[3px] text-[11.5px] leading-normal", TONE_TEXT[c.tone])} aria-live="polite">
          {c.detail}
        </div>
        {c.evidence && <div className="mt-0.5 font-mono text-[11px] text-text-faint">{c.evidence}</div>}
        {dns.isError && (
          <div className="mt-0.5 text-[11px] text-text-faint">
            Couldn&rsquo;t read the DNS provider&rsquo;s state.{" "}
            <button type="button" className="underline underline-offset-2" onClick={() => void dns.refetch()}>
              Try again
            </button>
          </div>
        )}
        {check.isError && (
          <div className="mt-0.5 text-[11px] text-danger">
            The check couldn&rsquo;t run — the panel needs outbound internet access from its host. Try again once it
            has it.
          </div>
        )}
      </div>
      <span className={cn("shrink-0 whitespace-nowrap font-mono text-[10.5px] font-medium", TONE_CHIP[c.tone])}>
        {c.chip}
      </span>
      {/* The one verb the row has. Without a DNS provider the dot would stay
          hollow forever otherwise — the canvas's amber row is exactly the
          case where someone has to ask the question. */}
      <ActionButton
        size="sm"
        variant="ghost"
        state={check.isFetching ? "busy" : "idle"}
        busyLabel="Checking…"
        onClick={() => void check.refetch()}
        className="-mr-2 shrink-0"
      >
        {check.data || check.isError ? "Check again" : "Check DNS"}
      </ActionButton>
    </div>
  );
}
