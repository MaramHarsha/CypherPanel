// The Domain field, when the panel knows which domains you actually own
// (docs/features/dns-automation.md §6, design canvas 17b).
//
// Free text was the wrong shape once a DNS provider is connected. Typing a
// whole hostname invites `google.com` — which the panel will accept, store,
// and then quietly refuse to route, leaving an application that looks fine and
// serves nothing. The zones Cloudflare returns ARE the list of domains you can
// use, so the field offers them: one joined control, a subdomain box with the
// zone picker attached to its right edge, which can only produce a hostname
// that verifies. The verification state of the saved value sits directly
// beneath it (DomainVerification).
//
// A custom domain stays possible, because someone will always have a zone
// managed elsewhere. It is a deliberate second choice, and it says plainly and
// permanently that it is not verified — never a silent acceptance.
import { useEffect, useMemo, useState } from "react";
import { useGetApplicationDNS } from "@/api/gen/applications/applications";
import { DomainVerification } from "@/components/domain-verification";
import { StatusDot } from "@/components/status-badge";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";

/** Split a hostname into its subdomain and the zone it belongs to. */
export function splitDomain(domain: string, zones: string[]): { sub: string; zone: string } | null {
  const host = domain.trim().toLowerCase().replace(/\.$/, "");
  if (!host) return null;
  // Longest zone wins, on label boundaries — the same rule the server matches
  // with, so what the picker shows and what verification decides cannot drift.
  let best: string | null = null;
  for (const z of zones) {
    const zone = z.toLowerCase();
    if (host === zone || host.endsWith(`.${zone}`)) {
      if (!best || zone.length > best.length) best = zone;
    }
  }
  if (!best) return null;
  return { sub: host === best ? "" : host.slice(0, -(best.length + 1)), zone: best };
}

export function DomainField({
  applicationId,
  value,
  onChange,
}: {
  applicationId: string;
  value: string;
  onChange: (next: string) => void;
}) {
  const { data } = useGetApplicationDNS(applicationId);
  const zones = useMemo(() => data?.available_zones ?? [], [data]);
  const parsed = useMemo(() => splitDomain(value, zones), [value, zones]);

  // Custom mode is sticky once chosen, and starts on when the current value is
  // a domain we do not manage — otherwise editing an existing custom domain
  // would silently rewrite it into a zone the operator never picked.
  const [custom, setCustom] = useState(false);
  const [touched, setTouched] = useState(false);
  useEffect(() => {
    if (!touched && value && zones.length > 0 && !parsed) setCustom(true);
  }, [touched, value, zones, parsed]);

  // No provider connected: nothing is enforced, so nothing changes. This is the
  // whole reason the feature is safe to add to an existing install.
  if (!data?.enforced || zones.length === 0) {
    return (
      <Field label="Domain" hint="Where the app is reachable.">
        {(id) => (
          <Input
            id={id}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            className="mono"
            autoComplete="off"
            spellCheck={false}
          />
        )}
      </Field>
    );
  }

  if (custom) {
    return (
      <div>
        <Field label="Domain" qualifier="· custom, outside Cloudflare">
          {(id) => (
            <Input
              id={id}
              value={value}
              onChange={(e) => onChange(e.target.value)}
              className="mono"
              autoComplete="off"
              spellCheck={false}
            />
          )}
        </Field>
        {/* The draft's own verdict, worded the way the saved one renders: a
            zone managed elsewhere is permanently unverified here. */}
        <p className="mt-2 flex items-start gap-2 text-[12px] leading-[1.5] text-text-dim">
          <StatusDot status="degraded" className="mt-[5px] h-2 w-2" />
          <span className="min-w-0">
            Verification pending in Cloudflare —{" "}
            <s className="font-mono text-[11.5px]">{value.trim() || "this domain"}</s> is outside your zones (
            <span className="font-mono text-[11.5px]">{zones.join(", ")}</span>) · not routed, no cert requested ·{" "}
            <button
              type="button"
              className="font-semibold underline-offset-2 hover:underline"
              onClick={() => {
                setTouched(true);
                setCustom(false);
                onChange(zones[0] ?? "");
              }}
            >
              choose a domain you own
            </button>
          </span>
        </p>
      </div>
    );
  }

  const sub = parsed?.sub ?? "";
  const zone = parsed?.zone ?? zones[0] ?? "";
  const compose = (nextSub: string, nextZone: string) => {
    const s = nextSub.trim().replace(/^\.+|\.+$/g, "");
    onChange(s ? `${s}.${nextZone}` : nextZone);
  };

  return (
    <div>
      <Field label="Domain" qualifier="· verified in Cloudflare">
        {(id) => (
          // One outline around both halves: the box draws the control's border
          // and its focus, the halves draw none of their own, and the zone
          // segment is sunken so it reads as attached rather than typed into.
          <span className="flex max-w-[440px] items-stretch rounded-md border border-border-input bg-surface transition-colors focus-within:border-border-strong focus-within:ring-1 focus-within:ring-border-strong">
            <Input
              id={id}
              value={sub}
              placeholder="app"
              aria-label="Subdomain"
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => {
                setTouched(true);
                compose(e.target.value, zone);
              }}
              className="mono min-w-0 flex-1 rounded-r-none border-0 bg-transparent focus-visible:ring-0"
            />
            <Select
              value={zone}
              aria-label="Zone"
              onChange={(e) => {
                setTouched(true);
                compose(sub, e.target.value);
              }}
              className="mono w-auto shrink-0 rounded-l-none border-0 border-l border-border-input bg-raised text-text-dim focus-visible:ring-0"
            >
              {zones.map((z) => (
                <option key={z} value={z}>
                  .{z}
                </option>
              ))}
            </Select>
          </span>
        )}
      </Field>
      <DomainVerification applicationId={applicationId} />
      <p className="mt-1.5 text-[11.5px] leading-relaxed text-text-faint">
        Leave the first box empty to use <span className="mono">{zone}</span> itself. Need a domain managed elsewhere?{" "}
        <button
          type="button"
          className="underline underline-offset-2"
          onClick={() => {
            setTouched(true);
            setCustom(true);
          }}
        >
          Use a custom domain
        </button>
        .
      </p>
    </div>
  );
}
