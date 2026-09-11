# Feature spec: Two-factor authentication (TOTP + recovery codes)

> The feature-matrix V1 row "Two-factor authentication (TOTP + recovery codes)".
> Panel compromise means fleet control (threat-model asset A3/A4), so account
> security is not optional here — a leaked password alone must not be enough.
>
> Written 2026-07-21, just before implementation (CLAUDE.md rule 7). Vocabulary
> per [glossary.md](../glossary.md).

## 1. Model

Two-factor is per-user and standards-based: **TOTP** (RFC 6238 — HMAC-SHA1,
6 digits, 30-second step), the scheme every authenticator app (Google
Authenticator, 1Password, Aegis…) speaks. We implement the RFC directly in the
standard library rather than take a dependency: the construction is small, and
its correctness is pinned to the RFC 6238 Appendix B test vectors in
`core/auth/totp_test.go` — smaller footprint (vision budgets), no supply chain,
fully understood.

State lives on the existing `users` columns plus one flag and one table:

```
users.totp_secret_enc / totp_secret_nonce   the secret, sealed with the master
                                             key (secret.Box, AES-256-GCM — the
                                             same box that protects the CA key)
users.totp_enabled                           false until a code is verified
totp_recovery_codes(user_id, code_hash, used_at)   single-use fallbacks, hashed
```

**Nothing is stored in the clear.** The secret is sealed at rest; recovery codes
are stored only as SHA-256 hashes (like sessions, join tokens, and API tokens).
A database read yields no usable second factor.

## 2. Enrollment lifecycle

Enrollment is two-step so a half-finished setup can never lock anyone out:

1. **`POST /auth/totp/enroll`** — generates a fresh secret, seals it, stores it
   with `totp_enabled = false`, and returns the secret (base32, for manual
   entry) plus an `otpauth://` URI the client renders as a QR code. We never
   generate QR images server-side. Re-enrolling before verification just
   replaces the pending secret; enrolling **while already enabled is refused
   (409)** so an active secret is never silently rotated away.
2. **`POST /auth/totp/verify` `{code}`** — validates the code against the
   pending secret (±1 step for clock drift, constant-time compare), flips
   `totp_enabled = true`, and returns **ten single-use recovery codes, shown
   exactly once**. A wrong code changes nothing.

**`POST /auth/totp/disable` `{code}`** turns 2FA off and clears recovery codes,
but **requires a valid authenticator or recovery code** — an authenticated
session alone cannot strip the second factor (defends against a stolen session).

**`GET /auth/totp`** reports `{enabled, recovery_codes_left}` for the account UI.

## 3. Login with a second factor

Login is single-step and stateless — no server-side challenge token:

```
POST /auth/login {email, password, totp_code?}
```

The authenticator verifies the password first (unknown-user timing defence
unchanged). Then, for a 2FA-enabled account:

- **no `totp_code`** → `401` with body `{"totp_required": true}`. The password
  was correct; the client re-prompts for a code only (it need not re-send the
  password). This reveals "2FA is on" only *after* a correct password — an
  acceptable, standard trade-off.
- **wrong `totp_code`** → `401 invalid credentials`, and it **counts against the
  login rate limiter** (a missing code does not — it is a benign first step).
- **valid authenticator code, or an unused recovery code** → session issued. A
  recovery code is **consumed atomically** (single-use; the SQL update matches
  only `used_at IS NULL` and returns the row, so a concurrent reuse cannot
  double-spend).

## 3a. Enrollment UI

Settings → Account carries the whole flow, and it is deliberately linear so
enrollment cannot be left half-finished: **turn on** → the panel returns a
pending secret, rendered as a QR code *and* as a copyable key for manual entry
→ **confirm a code** → the ten recovery codes are shown once, with a copy
control and an explicit "I saved them" acknowledgement. The server only enables
2FA after a verified code, so abandoning the dialog at any point leaves the
account exactly as it was.

The QR is drawn client-side as inline SVG from the `otpauth_uri`
(`web/src/components/qr-code.tsx`) — an enrollment secret never travels to a
third-party image service, which is the entire point of self-hosting.

Turning 2FA **off** demands a live factor (an authenticator code or an unused
recovery code) in the same dialog, and the route — like the rest of credential
management — is reachable only from an interactive session, never an API token
([api-tokens.md](api-tokens.md) §1).

## 4. Security properties (threat-model §5)

- Secret sealed at rest; recovery codes hashed; both shown/entered once.
- Disable and login both demand possession of a live factor.
- Recovery codes are single-use and race-safe (atomic consume).
- Constant-time code comparison; ±1-step skew only (no wide window).
- 2FA columns cascade with the user; recovery codes `ON DELETE CASCADE`.
- Wrong second factors are rate-limited exactly like wrong passwords.

## 5. Out of scope this slice

- WebAuthn / hardware keys, SMS/email OTP (TOTP is the V1 row).
- Per-user "remember this device" trust cookies.
- Admin-forced 2FA enrollment policy (an org-wide requirement toggle) — the
  mechanism here is the prerequisite; the policy layer is a follow-on.

## 6. Acceptance (testable)

1. RFC 6238 Appendix B vectors pass (`core/auth/totp_test.go`) — the truncation
   and counter math are provably correct.
2. Enroll → verify with a real code enables 2FA and returns 10 recovery codes;
   a wrong code does not enable (auth service test).
3. Once enabled, login without a code returns `totp_required`; with a valid
   authenticator code succeeds; a recovery code succeeds once then is spent.
4. Disable requires a valid factor; afterwards login needs no code.
5. Real-Postgres store test covers enroll/enable/consume/disable and the
   single-use recovery-code semantics.
