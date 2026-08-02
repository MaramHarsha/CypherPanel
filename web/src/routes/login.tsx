// The unauthenticated entry point. It first asks the panel whether it needs
// first-run setup (first-run-setup.md §3): a brand-new panel shows "Create your
// admin account"; an established one shows sign-in.
//
// Mission Control splits the screen: an ink panel carrying the wordmark and
// the three promises the product actually keeps, and a paper panel carrying
// the one form. The ink half is decoration only in the sense that a masthead
// is — it tells a first-time self-hoster what they just installed.
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useGetSetupStatus, useLogin, useSetup } from "@/api/gen/auth/auth";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { setToken } from "@/lib/auth";

interface LoginSearch {
  return?: string;
}

export const Route = createFileRoute("/login")({
  validateSearch: (s: Record<string, unknown>): LoginSearch =>
    typeof s.return === "string" && s.return.startsWith("/") ? { return: s.return } : {},
  component: EntryPage,
});

const PROMISES = ["push code, get a URL", "zero-downtime by default", "no SSH keys stored, ever"];

function EntryPage() {
  const setup = useGetSetupStatus({ query: { staleTime: 0, retry: false } });

  return (
    <main className="flex min-h-dvh bg-bg">
      {/* The ink half — masthead, not chrome. Folded away below `md`. */}
      <aside className="hidden w-[380px] shrink-0 flex-col bg-[#16130e] px-8 py-9 text-[#e9e5dc] md:flex">
        <span className="text-[17px] font-bold tracking-tight text-[#faf8f4]">
          Cypher<span className="text-[#ff6a33]">Panel</span>
        </span>
        <ul className="mt-auto space-y-1.5 font-mono text-[12.5px] text-[#8a8375]">
          {PROMISES.map((p) => (
            <li key={p}>
              <span className="text-[#5cbf7f]">✓</span> {p}
            </li>
          ))}
        </ul>
        <p className="mt-7 font-mono text-[11px] text-[#5d5850]">your servers · your data</p>
      </aside>

      <div className="flex min-w-0 flex-1 items-center justify-center px-5 py-10">
        <div className="w-full max-w-[320px]">
          {/* The wordmark rides along on narrow screens, where the ink half is gone. */}
          <span className="mb-8 block text-[17px] font-bold tracking-tight md:hidden">
            Cypher<span className="text-accent">Panel</span>
          </span>
          {setup.isPending ? (
            <div className="space-y-4" aria-busy>
              <Skeleton className="h-7 w-40" />
              <Skeleton className="h-9" />
              <Skeleton className="h-9" />
              <Skeleton className="h-10 rounded-full" />
            </div>
          ) : setup.data?.needs_setup ? (
            <SetupForm />
          ) : (
            <LoginForm />
          )}
        </div>
      </div>
    </main>
  );
}

function FormError({ message }: { message: string }) {
  return (
    <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
      {message}
    </p>
  );
}

function SetupForm() {
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);

  const setup = useSetup({
    mutation: {
      onSuccess: (res) => {
        setToken(res.token);
        void navigate({ to: "/projects", replace: true });
      },
      onError: (err: unknown) => {
        if (err instanceof ApiError) {
          // Someone claimed the panel in a race — fall back to login.
          if (err.status === 409) window.location.reload();
          else setError(err.message);
        } else {
          setError("Could not reach the server — check that cypherd is running");
        }
      },
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (password !== confirm) {
      setError("Passwords don't match");
      return;
    }
    if (password.length < 8) {
      setError("Password must be at least 8 characters");
      return;
    }
    setup.mutate({ data: { email, password } });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <div>
        <p className="eyebrow">fresh panel · no accounts yet</p>
        <h1 className="mt-3 text-[22px] font-bold leading-tight tracking-tight text-text">
          Create your admin account
        </h1>
        <p className="mt-1.5 text-[12.5px] leading-relaxed text-text-mid">
          You'll be the owner of this panel. This screen appears exactly once — after this, it's sign-in only.
        </p>
      </div>
      <Field label="Email">
        {(id) => (
          <Input
            id={id}
            type="email"
            autoComplete="username"
            required
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        )}
      </Field>
      <Field label="Password" hint="At least 8 characters.">
        {(id) => (
          <Input
            id={id}
            type="password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        )}
      </Field>
      <Field label="Confirm password">
        {(id) => (
          <Input
            id={id}
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        )}
      </Field>
      {error && <FormError message={error} />}
      <Button type="submit" variant="accent" size="lg" className="w-full" disabled={setup.isPending}>
        {setup.isPending ? "Creating…" : "Create account & sign in →"}
      </Button>
    </form>
  );
}

function LoginForm() {
  const navigate = useNavigate();
  const search = Route.useSearch();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [needsTotp, setNeedsTotp] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const login = useLogin({
    mutation: {
      onSuccess: (res) => {
        setToken(res.token);
        void navigate({ to: search.return ?? "/projects", replace: true });
      },
      onError: (err: unknown) => {
        if (err instanceof ApiError) {
          if (err.status === 401 && err.message.toLowerCase().includes("code")) {
            setNeedsTotp(true);
            setError(totp ? err.message : null);
            return;
          }
          setError(err.message);
        } else {
          setError("Could not reach the server — check that cypherd is running");
        }
      },
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    login.mutate({ data: { email, password, ...(totp ? { totp_code: totp } : {}) } });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <h1 className="text-[24px] font-bold leading-none tracking-tight text-text">Sign in</h1>
      <Field label="Email">
        {(id) => (
          <Input
            id={id}
            type="email"
            autoComplete="email"
            required
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        )}
      </Field>
      <Field label="Password">
        {(id) => (
          <Input
            id={id}
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        )}
      </Field>
      {needsTotp && (
        <Field label="Authenticator code" hint="The 6-digit code from your authenticator app, or a recovery code.">
          {(id) => (
            <Input
              id={id}
              inputMode="numeric"
              autoComplete="one-time-code"
              autoFocus
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
            />
          )}
        </Field>
      )}
      {error && <FormError message={error} />}
      <Button type="submit" variant="accent" size="lg" className="w-full" disabled={login.isPending}>
        {login.isPending ? "Signing in…" : "Sign in →"}
      </Button>
    </form>
  );
}
