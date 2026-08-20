// Full-screen panel errors — design canvas 8a–8e (read from their dark twins
// 13p–13t; every value maps through the token sheet, so one component serves
// both themes).
//
// These replace nothing: the panel had inline error regions but no full-page
// answer for "this route does not exist" or "cypherd is unreachable", so those
// landed on a blank screen or a spinner that never resolved.
//
// The shared shape is a 96px mono numeral with its middle digit in accent, a
// 19px headline that says what happened in the operator's terms, one
// explanatory line, and actions that are never a dead end (ui-principles §11).
import { Link } from "@tanstack/react-router";
import { type ReactNode } from "react";
import { openCommandPalette } from "@/components/command-palette";
import { cn } from "@/lib/utils";

function Frame({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-bg px-8 py-10 text-center text-text">
      {children}
    </div>
  );
}

/** The 4·0·4 numeral: middle digit accent, the rest ink. */
function Numeral({ code }: { code: string }) {
  const [a, b, c] = code.split("");
  return (
    <div className="font-mono text-[96px] leading-none tracking-[-0.04em]" aria-hidden>
      {a}
      <span className="text-accent">{b}</span>
      {c}
    </div>
  );
}

function Headline({ children }: { children: ReactNode }) {
  return (
    <h1 className="mb-1.5 mt-4 text-[19px] font-bold tracking-[-0.02em] text-text">{children}</h1>
  );
}

function Explain({ children, wide }: { children: ReactNode; wide?: boolean }) {
  return (
    <p className={cn("text-[13px] leading-[1.55] text-text-mid", wide ? "max-w-[370px]" : "max-w-[340px]")}>
      {children}
    </p>
  );
}

function Actions({ children }: { children: ReactNode }) {
  return <div className="mt-[22px] flex flex-wrap items-center justify-center gap-2.5">{children}</div>;
}

/** The filled pill: paper-on-ink in dark, ink-on-paper in light. */
const solid =
  "rounded-full bg-primary px-[18px] py-[9px] text-[12.5px] font-semibold text-primary-fg hover:bg-primary-hover";
/** The outlined pill beside it. */
const outline =
  "rounded-full border border-border-strong bg-surface px-[18px] py-[9px] text-[12.5px] font-semibold text-text hover:bg-raised";

/** 8a — 404. Two meanings: never existed, or a teammate deleted it. */
export function NotFoundPage({
  resource,
  auditLogHref,
}: {
  resource?: string;
  /** Set once the audit log page lands; omitted, the third action is hidden. */
  auditLogHref?: string;
}) {
  return (
    <Frame>
      <Numeral code="404" />
      <Headline>This page doesn't exist.</Headline>
      <Explain>
        Or it did — {resource ? <span className="mono text-[12px]">{resource}</span> : "the resource"} may have
        been deleted by a teammate. The audit log remembers.
      </Explain>
      <Actions>
        <Link to="/projects" className={solid}>
          ← Projects
        </Link>
        {/* ⌘K is the palette the panel already ships; this is the same door,
            opened by asking for it rather than by faking the keystroke. */}
        <button type="button" className={outline} onClick={openCommandPalette}>
          ⌘K Search
        </button>
        {/* The third action the canvas draws. It is what makes "deleted by a
            teammate" actionable rather than a shrug — but it is only offered
            once the audit log exists (canvas 3g, V1.x): a 404 whose way out is
            another 404 is the dead end this page exists to avoid. */}
        {auditLogHref && (
          <Link to={auditLogHref} className={outline}>
            Audit log
          </Link>
        )}
      </Actions>
    </Frame>
  );
}

/**
 * 8b — 403. The design is emphatic that this page *names the fix*: which
 * action, which role it needs, which role you hold, and who can grant it. A
 * 403 that only says "forbidden" makes the operator guess.
 */
export function ForbiddenPage({
  action = "Deploying to production",
  needs = "developer",
  held = "viewer",
  owners = [],
  onRequestAccess,
}: {
  action?: string;
  needs?: string;
  held?: string;
  owners?: string[];
  onRequestAccess?: () => void;
}) {
  return (
    <Frame>
      <Numeral code="403" />
      <Headline>You can see this, but not touch it.</Headline>
      <Explain wide>
        <b className="text-text">{action}</b> needs the <b className="text-text">{needs}</b> role — you're a{" "}
        <b className="text-text">{held}</b> on this team.
      </Explain>
      <Actions>
        {owners[0] && (
          <button type="button" className={solid} onClick={onRequestAccess}>
            Request access from {owners[0].split("@")[0]}@
          </button>
        )}
        <button type="button" className={outline} onClick={() => window.history.back()}>
          Back
        </button>
      </Actions>
      {owners.length > 0 && (
        <p className="mono mt-[18px] text-[11px] text-text-faint">owners: {owners.join(", ")}</p>
      )}
    </Frame>
  );
}

/**
 * 8c — control plane unreachable. Deliberately calm: the single most important
 * fact is that the fleet is unaffected, because routing and containers live on
 * the servers. Panic here would be a lie about the blast radius.
 */
export function PlaneOfflinePage({
  retryInSeconds,
  lastSyncLabel,
  onRetry,
}: {
  retryInSeconds?: number;
  lastSyncLabel?: string;
  onRetry?: () => void;
}) {
  return (
    <Frame>
      <div
        className="flex size-16 items-center justify-center rounded-full border-[1.5px] border-dashed border-text-faint font-mono text-2xl text-text-faint"
        aria-hidden
      >
        ⇅
      </div>
      <Headline>Can't reach the control plane.</Headline>
      <Explain wide>
        <b className="text-text">Your apps are still serving.</b> Routing and containers live on the servers,
        not here — the panel is only the steering wheel.
      </Explain>
      <div className="mono mt-5 flex items-center gap-[9px] text-[12px] text-text-faint">
        <span className="size-[7px] flex-none rounded-full bg-status-degraded" aria-hidden />
        {retryInSeconds !== undefined ? `retrying in ${retryInSeconds}s` : "retrying"}
        {lastSyncLabel ? ` · last sync ${lastSyncLabel}` : ""}
      </div>
      <button type="button" className={cn(outline, "mt-3.5")} onClick={onRetry ?? (() => location.reload())}>
        Retry now
      </button>
    </Frame>
  );
}

/** 8d — 500. Carries a trace id worth pasting into an issue. */
export function ServerFaultPage({ traceId, onReport }: { traceId?: string; onReport?: () => void }) {
  return (
    <Frame>
      <Numeral code="500" />
      <Headline>The panel hit a bug. Your fleet didn't.</Headline>
      <Explain>
        This request failed inside cypherd. It's logged — attach the trace id if you file an issue.
      </Explain>
      {traceId && (
        <div className="mono mt-[18px] flex items-center gap-2.5 rounded-md bg-toast px-3.5 py-[9px] text-[12px] text-toast-text">
          {traceId}
          <button
            type="button"
            onClick={() => void navigator.clipboard?.writeText(traceId)}
            className="rounded border border-pane-border px-2 py-0.5 text-[10.5px] hover:bg-white/5"
          >
            copy
          </button>
        </div>
      )}
      <Actions>
        <button type="button" className={solid} onClick={() => location.reload()}>
          Reload
        </button>
        <button type="button" className={outline} onClick={onReport}>
          Report issue ↗
        </button>
      </Actions>
    </Frame>
  );
}

/**
 * 8e — 429. Grounded in core/auth/ratelimit.go, which already throttles login.
 * The bar drains toward zero so the wait is legible without reading the clock.
 */
export function ThrottledPage({
  secondsLeft,
  totalSeconds = 300,
}: {
  secondsLeft: number;
  totalSeconds?: number;
}) {
  const mm = Math.floor(secondsLeft / 60);
  const ss = String(secondsLeft % 60).padStart(2, "0");
  return (
    <Frame>
      <Numeral code="429" />
      <Headline>Too many attempts.</Headline>
      <Explain>
        Sign-in for this account is paused after repeated failures. Try again in{" "}
        <b className="mono text-text">
          {mm}:{ss}
        </b>{" "}
        — or use a recovery code if you've lost your device.
      </Explain>
      <div className="mt-5 h-1.5 w-60 overflow-hidden rounded-full bg-raised">
        <div
          className="h-full rounded-full bg-accent transition-[width] duration-1000"
          style={{ width: `${Math.min(100, Math.max(0, (secondsLeft / totalSeconds) * 100))}%` }}
        />
      </div>
      <p className="mono mt-4 text-[11px] text-text-faint">every failure is in the audit log</p>
    </Frame>
  );
}
