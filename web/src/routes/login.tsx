// The unauthenticated entry point. It first asks the panel whether it needs
// first-run setup (first-run-setup.md §3): a brand-new panel shows "Create your
// admin account"; an established one shows sign-in.
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useGetSetupStatus, useLogin, useSetup } from "@/api/gen/auth/auth";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { setToken } from "@/lib/auth";

interface LoginSearch {
  return?: string;
}

export const Route = createFileRoute("/login")({
  validateSearch: (s: Record<string, unknown>): LoginSearch =>
    typeof s.return === "string" && s.return.startsWith("/") ? { return: s.return } : {},
  component: EntryPage,
});

function EntryPage() {
  const setup = useGetSetupStatus({ query: { staleTime: 0, retry: false } });

  return (
    <main className="flex min-h-dvh items-center justify-center bg-bg p-4">
      <div className="w-full max-w-sm">
        <p className="mono mb-8 text-center text-sm tracking-wide text-text">
          <span className="text-accent">▲</span> cypherpanel
        </p>
        {setup.isPending ? (
          <div className="h-40 animate-pulse rounded-lg border border-border bg-surface" aria-hidden />
        ) : setup.data?.needs_setup ? (
          <SetupForm />
        ) : (
          <LoginForm />
        )}
      </div>
    </main>
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
    <form onSubmit={submit} className="space-y-4 rounded-lg border border-border bg-surface p-6">
      <div>
        <h1 className="text-sm font-semibold text-text">Create your admin account</h1>
        <p className="mt-1 text-xs text-text-mid">
          This is a fresh panel. The first account you make is the owner — it can manage everything.
        </p>
      </div>
      <Field label="Email">
        {(id) => (
          <Input id={id} type="email" autoComplete="username" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
        )}
      </Field>
      <Field label="Password" hint="At least 8 characters.">
        {(id) => (
          <Input id={id} type="password" autoComplete="new-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        )}
      </Field>
      <Field label="Confirm password">
        {(id) => (
          <Input id={id} type="password" autoComplete="new-password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        )}
      </Field>
      {error && (
        <p role="alert" className="text-[13px] text-danger">
          {error}
        </p>
      )}
      <Button type="submit" variant="primary" size="lg" className="w-full" disabled={setup.isPending}>
        {setup.isPending ? "Creating…" : "Create account & sign in"}
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
    <form onSubmit={submit} className="space-y-4 rounded-lg border border-border bg-surface p-6">
      <Field label="Email">
        {(id) => (
          <Input id={id} type="email" autoComplete="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
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
              className="mono"
            />
          )}
        </Field>
      )}
      {error && (
        <p role="alert" className="text-[13px] text-danger">
          {error}
        </p>
      )}
      <Button type="submit" variant="primary" size="lg" className="w-full" disabled={login.isPending}>
        {login.isPending ? "Signing in…" : "Sign in"}
      </Button>
    </form>
  );
}
