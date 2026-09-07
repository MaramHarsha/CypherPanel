// Accepting an invitation. This is the other end of `POST /teams/{id}/invites`
// — the link the create dialog hands over is `<panel>/invite/<token>`, and
// without a route here that link goes nowhere, which makes the whole feature
// undeliverable.
//
// It is unauthenticated by construction: the invitee has no account yet, or has
// one they are not signed into. That is why it lives beside `/login` rather
// than under `_app`.
//
// Two facts shape the form, and both come from the preview rather than from
// anything typed here:
//
//   · The ADDRESS is fixed. The invitation was issued for one, it cannot be
//     changed, and offering an email field would imply otherwise.
//   · `account_exists` decides which password is being asked for. For a new
//     account it is one being chosen; for an existing one it is that account's
//     CURRENT password — accepting an invitation is never a password reset,
//     and the second factor still applies. Saying which of the two this is,
//     before the field, is the difference between a form and a trap.
//
// Every failure the panel can give — unknown token, wrong secret, expired,
// revoked, already spent — comes back as one undifferentiated 404, on purpose:
// distinguishing them would turn this public route into an oracle. So the page
// says the one honest thing it can and points at the person who can reissue.
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useAcceptInvite, useGetInvite } from "@/api/gen/invites/invites";
import { ThrottledPage, useSecondsLeft } from "@/components/error-page";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { setToken } from "@/lib/auth";
import { relativeTime } from "@/lib/time";

export const Route = createFileRoute("/invite/$token")({ component: InvitePage });

function InvitePage() {
  const { token } = Route.useParams();
  // No retry: the one failure this route has is a 404, and retrying it three
  // times only delays the sentence that explains it.
  const preview = useGetInvite(token, { query: { retry: false, staleTime: 0 } });

  return (
    <main className="flex min-h-dvh bg-bg">
      <aside className="hidden w-[360px] shrink-0 flex-col bg-toast px-8 py-[34px] text-toast-text md:flex">
        <span className="text-[17px] font-bold tracking-tight text-[#faf8f4]">
          Cypher<span className="text-[#ff6a33]">Panel</span>
        </span>
        <p className="mt-auto max-w-[220px] font-mono text-[12.5px] leading-[1.7] text-toast-faint">
          A team owns servers and projects. Joining one gives you what its role allows and nothing else.
        </p>
        <p className="mt-[26px] font-mono text-[11px] text-toast-dismiss">your servers · your data</p>
      </aside>

      <div className="flex min-w-0 flex-1 items-center justify-center px-5 py-10">
        <div className="w-full max-w-[300px]">
          <span className="mb-8 block text-[17px] font-bold tracking-tight md:hidden">
            Cypher<span className="text-accent">Panel</span>
          </span>
          {preview.isPending ? (
            <div className="space-y-4" aria-busy>
              <Skeleton className="h-7 w-40" />
              <Skeleton className="h-9" />
              <Skeleton className="h-10 rounded-full" />
            </div>
          ) : preview.isError ? (
            <DeadLink />
          ) : (
            <AcceptForm token={token} preview={preview.data} />
          )}
        </div>
      </div>
    </main>
  );
}

/**
 * One page for five different refusals, because the API deliberately answers
 * one 404 for all of them. Guessing which it was — "expired", "already used" —
 * would be the panel inventing a fact, and ui-principles §10 does not allow it.
 */
function DeadLink() {
  return (
    <div>
      <h1 className="mb-3 text-[24px] font-bold leading-none tracking-tight text-text">This link no longer works</h1>
      <p className="text-[13px] leading-[1.6] text-text-mid">
        An invitation link is single-use and expires after seven days. It also stops working if it was revoked, or if
        someone has already accepted it. Ask whoever invited you to send a new one.
      </p>
      <Link to="/login" className="mt-5 inline-block text-[13px] font-medium text-text underline">
        Sign in instead
      </Link>
    </div>
  );
}

function AcceptForm({
  token,
  preview,
}: {
  token: string;
  preview: {
    team_name: string;
    inviter_label: string;
    email: string;
    role: string;
    expires_at: string;
    account_exists: boolean;
  };
}) {
  const navigate = useNavigate();
  const existing = preview.account_exists;
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [totp, setTotp] = useState("");
  const [needsTotp, setNeedsTotp] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The route is throttled by client address, like login, and for the same
  // reason: it is public and it takes a secret.
  const [throttle, setThrottle] = useState<{ until: number | null; total: number | undefined } | null>(null);
  const secondsLeft = useSecondsLeft(throttle?.until);
  // A pause with a deadline ends by itself, like login's. ThrottledPage only
  // offers a way out when there is no countdown, so without this the invitee
  // watches the bar drain and is then left on a page with no form and no pill.
  useEffect(() => {
    if (throttle?.until && secondsLeft === 0) setThrottle(null);
  }, [throttle, secondsLeft]);

  const accept = useAcceptInvite({
    mutation: {
      // Accepting signs you in — the invitation carries the session, so the
      // path ends on the team rather than back at a login form asking for the
      // password just typed.
      onSuccess: (res) => {
        setToken(res.token);
        void navigate({ to: "/projects", replace: true });
      },
      onError: (err: unknown) => {
        if (err instanceof ApiError) {
          if (err.status === 401 && err.message.toLowerCase().includes("code")) {
            setNeedsTotp(true);
            setError(totp ? err.message : null);
            return;
          }
          if (err.status === 429) {
            const s = err.retryAfterSeconds;
            setThrottle({ until: s ? Date.now() + s * 1000 : null, total: s });
            return;
          }
          setError(err.message);
          return;
        }
        setError("Could not reach the server — check that cypherd is running");
      },
    },
  });
  const state = useMutationActionState(accept);

  if (throttle) {
    return (
      <ThrottledPage
        embedded
        secondsLeft={throttle.until ? secondsLeft : undefined}
        totalSeconds={throttle.total}
        onTryAgain={() => setThrottle(null)}
      />
    );
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    accept.mutate({
      token,
      data: {
        password,
        ...(existing || displayName.trim() === "" ? {} : { display_name: displayName.trim() }),
        ...(totp ? { totp_code: totp } : {}),
      },
    });
  };

  return (
    <form onSubmit={submit}>
      <h1 className="mb-2 text-[24px] font-bold leading-none tracking-tight text-text">
        Join {preview.team_name}
      </h1>
      <p className="mb-5 text-[13px] leading-[1.6] text-text-mid">
        {preview.inviter_label} invited you as <span className="mono text-text">{preview.role}</span>. This link expires{" "}
        {relativeTime(preview.expires_at)}.
      </p>

      {/* The address is the invitation. Showing it as a field you could edit
          would be a lie, so it is shown as what it is. */}
      <div className="mb-4 rounded-md border border-border bg-raised px-3 py-2">
        <p className="eyebrow">invited address</p>
        <p className="mono mt-0.5 truncate text-[13px] text-text">{preview.email}</p>
      </div>

      <div className="space-y-4">
        {existing ? (
          <Field
            label="Your password"
            hint="This address already has an account. Accepting joins it to the team — it never changes your password."
          >
            {(id, describedBy) => (
              <Input
                id={id}
                type="password"
                autoComplete="current-password"
                required
                autoFocus
                aria-describedby={describedBy}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            )}
          </Field>
        ) : (
          <>
            <Field label="Choose a password" hint="At least 8 characters.">
              {(id, describedBy) => (
                <Input
                  id={id}
                  type="password"
                  autoComplete="new-password"
                  required
                  autoFocus
                  minLength={8}
                  aria-describedby={describedBy}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              )}
            </Field>
            {/* Optional, and the panel joins you without it if it cannot take
                it — so it is asked for last and never blocks the join. */}
            <Field label="Display name" qualifier="· optional">
              {(id) => (
                <Input
                  id={id}
                  maxLength={64}
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  placeholder="Riya"
                />
              )}
            </Field>
          </>
        )}

        {/* An invitation is not a way around a second factor. */}
        {needsTotp && (
          <Field label="Authentication code" hint="From your authenticator app.">
            {(id) => (
              <Input
                id={id}
                inputMode="numeric"
                autoComplete="one-time-code"
                required
                autoFocus
                maxLength={6}
                value={totp}
                onChange={(e) => setTotp(e.target.value.replace(/\D/g, ""))}
              />
            )}
          </Field>
        )}

        {error && (
          <p role="alert" className="rounded-md border border-danger/40 bg-danger/[.06] px-3 py-2 text-[12.5px] text-danger">
            {error}
          </p>
        )}

        <ActionButton
          type="submit"
          variant="accent"
          size="lg"
          className="w-full"
          state={state}
          busyLabel="Joining…"
          successLabel="Joined"
          failedLabel="Try again"
        >
          {existing ? "Join the team →" : "Create account & join →"}
        </ActionButton>
      </div>
    </form>
  );
}
