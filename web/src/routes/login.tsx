import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { useLogin } from "@/api/gen/auth/auth";
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
  component: LoginPage,
});

function LoginPage() {
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
          // A 2FA account without a code gets the code field, not an error wall.
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
    <main className="flex min-h-dvh items-center justify-center bg-bg p-4">
      <div className="w-full max-w-sm">
        <p className="mono mb-8 text-center text-sm tracking-wide text-text">
          <span className="text-accent">▲</span> cypherpanel
        </p>
        <form onSubmit={submit} className="space-y-4 rounded-lg border border-border bg-surface p-6">
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
      </div>
    </main>
  );
}
