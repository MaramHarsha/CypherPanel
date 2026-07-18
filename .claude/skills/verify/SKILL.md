---
name: verify
description: Build, boot, and drive cypherd + cypher-agent end-to-end against a throwaway Postgres for runtime verification of control-plane/agent changes.
---

# Verifying CypherPanel end-to-end

No system Go may be present; a toolchain under the session scratchpad has
worked before (`.../scratchpad/go/bin`). Build per module — `go build ./...`
from the repo root fails (workspace layout): build `./cmd/cypherd` inside
`core/` and `./cmd/cypher-agent` inside `agent/`.

## Boot

```bash
docker run -d --name verify-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=cypher \
  -p 127.0.0.1:15432:5432 postgres:16-alpine
# wait: docker exec verify-pg pg_isready -U postgres

CYPHERD_DATABASE_URL="postgres://postgres:pw@127.0.0.1:15432/cypher?sslmode=disable" \
CYPHERD_MASTER_KEY=$(head -c32 /dev/urandom | base64) \
CYPHERD_HTTP_ADDR=127.0.0.1:18080 CYPHERD_ENROLL_ADDR=127.0.0.1:18443 \
CYPHERD_NATS_ADDR=127.0.0.1:14222 \
CYPHERD_ADMIN_EMAIL=owner@example.com CYPHERD_ADMIN_PASSWORD=verify-password-1 \
./cypherd   # migrates on boot; poll GET /readyz
```

`CYPHERD_PUBLIC_HOST` defaults to `localhost`, which matches the advertised
enroll/NATS addresses — keep everything on localhost and it just works.

## Drive

1. `POST /api/v1/auth/login` `{"email","password"}` → `{"token"}`; then
   `Authorization: Bearer <token>`.
2. `POST /api/v1/servers` `{"name"}` → server + `join.token` + ready-made
   `join.command`; `GET /api/v1/ca.pem` → save as `ca.pem`.
3. `cypher-agent enroll --plane localhost:18443 --token <jt> --ca-file ca.pem
   --state-dir <dir>` then `cypher-agent run --state-dir <dir> --heartbeat 2s`.
4. `GET /api/v1/servers/{id}` — status goes `unknown → running` within one
   heartbeat; `DELETE` kicks the live agent (its log shows `EOF` then
   `nats: authorization violation`; cypherd logs "refused connection from
   revoked or unknown identity").

## Console (browser)

The interim console is served at `GET /` and its rendering is pure JS —
curl can't verify it. `docker run -d --network host chromedp/headless-shell`
exposes CDP on :9222; a scratchpad Go module using
`chromedp.NewRemoteAllocator(ctx, "ws://127.0.0.1:9222/")` can log in
(`#email`, `#password`, `#login-form button[type=submit]`), wait for
`#servers-region table`, and assert/screenshot. Delete buttons are
`button[data-action="delete"]`; the confirm dialog is `#delete-dialog` with
`#delete-confirm` / `#delete-submit`.

## Gotchas

- `go build ./...` at the repo root: "directory prefix . does not contain
  modules listed in go.work" — always build inside each module (same for
  golangci-lint: from the root it silently checks nothing).
- A revoked agent exits on its own with code 1 ("bus connection closed
  permanently") within a few seconds of DELETE — assert the exit, not just
  the log line. Through a plain outage it never exits (infinite reconnect).
- Boot-time disk guard: `CYPHERD_MIN_DISK_FREE=999999999999999999` is a quick
  refusal probe; `0` disables; malformed values are a config error.
