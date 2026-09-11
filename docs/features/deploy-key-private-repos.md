# Feature spec: Deploy-key private repos

> Adds first-class SSH deploy key management so Applications can build from
> private Git repositories. The key lifecycle is API-driven; private key
> material is sealed at rest (same `secret.Box` as env vars and the CA key),
> decrypted only at build time, and transported only over the existing mTLS
> channel — never written to agent disk.
>
> Written 2026-07-19, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Resource model

A **DeployKey** is a reusable credential: one key may serve multiple
Applications (monorepo, organisation-wide deploy key). Applications reference
a deploy key by its id through the existing `source_deploy_key_id` FK column
([0002_deploy_slice.sql](../../core/store/migrations/0002_deploy_slice.sql)).

```
DeployKey:
  id            TEXT PK (dk_… prefix)
  name          TEXT NOT NULL            -- operator label ("my-org read-only")
  public_key    TEXT NOT NULL            -- OpenSSH authorized_keys format (for display / GitHub setup)
  fingerprint   TEXT NOT NULL UNIQUE     -- SHA-256 fingerprint for deduplication
  private_key_ct   BYTEA NOT NULL       -- sealed Ed25519 PEM (secret.Box, AES-256-GCM)
  private_key_nonce BYTEA NOT NULL
  created_at    TIMESTAMPTZ
```

The operator sees only the public key (at creation and on GET); the private
key is sealed before it reaches the store and never returned in any API
response (ENGINEERING rule 20).

## 2. Key generation

The control plane generates the keypair server-side (Ed25519, `crypto/ed25519`)
so the private key never crosses a network boundary in the clear. The public
key is returned once at creation for the operator to paste into GitHub / their
Git host. Alternative: accept an operator-supplied keypair — deliberately
deferred; server-side generation avoids the accidental-paste-in-Slack class of
key leaks.

## 3. API surface (under `/api/v1`)

```
POST   /deploy-keys             {name}              → DeployKey (+ public_key)
GET    /deploy-keys                                 → [DeployKey] (public_key only)
GET    /deploy-keys/{id}                            → DeployKey
DELETE /deploy-keys/{id}                            → 204
```

Deletion is `RESTRICT`-gated by the applications FK: a key in use cannot be
deleted until all referencing applications clear their `source_deploy_key_id`.
The 409 response names the blocking application(s): the `Error` envelope plus
`applications: [{id, name}]` (`DeployKeyInUse` in `openapi.yaml`), so the
operator can go detach them instead of clicking through every application
([control-plane-hardening.md](control-plane-hardening.md) §8).

Creating an application with `source.deploy_key_id` validates that the
referenced key exists (400 otherwise). PATCH-ing `source.deploy_key_id` to a
non-existent key is also 400.

## 4. Build-time key delivery

The scheduler populates `BuildWork.deploy_key_pem` (new proto field 8) with
the unsealed private key PEM when the Application's `source_deploy_key_id` is
set. The key travels only over the mTLS NATS channel (rule 23).

The builder agent:

1. Writes the PEM to a temp file (`<buildDir>/.deploy-key`, mode `0600`).
2. Sets `GIT_SSH_COMMAND=ssh -i <tmpfile> -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null`.
3. If the repo URL is `https://github.com/<owner>/<repo>`, rewrites it to
   `git@github.com:<owner>/<repo>.git` so SSH transport is used.
4. Runs `git clone` with the existing `gitEnv` + the SSH override.
5. Removes the temp file immediately after clone completes (deferred cleanup
   on all exit paths).

The key is **never** written to agent disk outside the ephemeral build
directory, which itself is `os.RemoveAll`'d at the end of `Build`.

## 5. Security

- Private keys are AES-256-GCM sealed at rest with the master key (same
  `secret.Box` as CA key and env vars; threat-model §5.1).
- Key material never appears in logs, error messages, or API responses
  (ENGINEERING rule 20). The `DisplayURL` redaction already in the builder
  covers the repo URL; deploy key errors report the key id, never the PEM.
- The unsealed PEM exists only in the scheduler's memory (during BuildWork
  construction) and in the builder's memory + one temp file (during clone).
  Both are short-lived.
- Fingerprint uniqueness prevents duplicate uploads.
- RESTRICT FK prevents orphan references.

## 6. Acceptance (testable)

1. Create a deploy key → the response includes the public key and the id.
2. Create an Application with `source.deploy_key_id` pointing at it.
3. `git push` to a **private** repo → the build clones successfully using the
   injected SSH key → deploy completes.
4. Delete the deploy key while an application references it → 409.
5. The private key PEM never appears in any log line, API response, or
   on-disk artifact outside the sealed store column and the ephemeral build dir.

## 7. Non-goals for this slice

Operator-supplied keypairs (accept a PEM upload) · RSA keys (Ed25519 only at
launch) · GitHub App installation tokens (a V1.x auth method, not a deploy
key) · agent-side key caching across builds (keys are ephemeral by design).

## Implementation note - push-to-deploy was unreachable *(fixed 2026-09-07)*

An operator reported that pushing to their branch did not deploy, and that they
had to press Deploy by hand every time. Nothing was misconfigured on their side.

`POST /webhooks/github/{id}` refuses any delivery whose `X-Hub-Signature-256`
does not verify - correctly, it is an unauthenticated endpoint and the signature
is the only thing in front of it. The secret it verifies against is minted when
the application is created and returned **exactly once**, in the create
response. The create dialog discarded that response's `webhook` object and
navigated away. The application's Overview then said *"Add this webhook to the
GitHub repository"* and showed the **URL alone**.

So the operator added a webhook with no secret, every delivery was answered
`401`, and no push ever deployed. No route read the secret back and none
replaced it, so the state was also unrecoverable: the only way to get a usable
secret was to delete the application and create another - which would have
discarded the new secret in exactly the same way.

**`POST /applications/{id}/webhook/rotate`** mints a new one and returns it once.
Rotating rather than revealing is deliberate: the stored value is sealed under
the master key, and a route that unseals a credential to display it is one that
eventually displays it to the wrong person. Pasting a new secret into GitHub is
work the operator is already doing. Team admin and interactive-session only,
because it is credential management and an API token must not mint one.

The Overview's card now asks for both halves, says that a delivery without a
valid signature is refused, and warns - before the button, not after - that
minting a new secret stops an already-configured webhook working until the new
one is pasted in.

**Separately, creating an application now deploys it.** The dialog's button says
*Deploy*, and it did not: it created the application and navigated to a page
showing `Stopped`, which is what produced the second half of the same report.
A failed first deploy is not a failed create - the application exists and its
own page has the button - so the failure is reported and the operator is left
where they can retry.
