# Deploying CypherPanel on a VPS

CypherPanel is a control plane (`cypherd` + PostgreSQL, with the web UI
embedded in the binary) plus agents (`cypher-agent`) that run on the servers
you deploy to. This guide brings up the **control plane** on a VPS. Agents join
afterwards from the UI with a one-line command (they install Docker themselves
— see [install/agent.sh](../../install/agent.sh)).

There are no hosted release binaries yet, so you build the plane from source or
its Docker image. Two paths — pick one.

---

## Prerequisites (both paths)

- A Linux VPS with a public IP and a DNS record (`panel.example.com`) pointing
  at it.
- Ports reachable from where operators and agents live:
  - **8080** — web UI + REST API (put TLS in front, see below)
  - **8443** — agent enrollment (gRPC, mTLS)
  - **4222** — agent data plane (NATS, mTLS)
- A **master key**, generated once and kept safe — it decrypts every secret at
  rest. Losing or changing it makes all sealed secrets unrecoverable:
  ```sh
  openssl rand -base64 32
  ```

---

## Path A — Docker Compose (recommended)

Runs `cypherd` and PostgreSQL as containers. Needs Docker + the compose plugin
on the VPS.

```sh
git clone https://github.com/MaramHarsha/cypherpanel && cd cypherpanel/deploy
cp cypherd.env.example cypherd.env
$EDITOR cypherd.env          # set POSTGRES_PASSWORD, CYPHERD_MASTER_KEY, CYPHERD_PUBLIC_HOST
docker compose up -d --build # builds the cypherd image (web UI embedded) and starts everything
docker compose logs -f cypherd
```

`cypherd` runs migrations on start and serves the UI at `http://<vps>:8080`.

## Path B — Binary + systemd (no Docker for the plane)

Build the binary (with the web UI embedded) and run it under systemd. Postgres
is a separate dependency — a managed database, a system package, or a
container.

```sh
# On a build machine (or the VPS) with Go 1.25+ and Node 22 + pnpm:
make build-web                                   # builds web/dist into the go:embed path
cd core && CGO_ENABLED=0 go build -o cypherd ./cmd/cypherd

# On the VPS:
sudo cp cypherd /usr/local/bin/
sudo cp install/cypherd.service /etc/systemd/system/
sudo mkdir -p /etc/cypherpanel
sudo cp deploy/cypherd.env.example /etc/cypherpanel/cypherd.env
sudo $EDITOR /etc/cypherpanel/cypherd.env        # MASTER_KEY, DATABASE_URL, PUBLIC_HOST
sudo systemctl enable --now cypherd
sudo journalctl -u cypherd -f
```

For Postgres via a container on the same box:
```sh
docker compose -f docker-compose.dev.yml up -d   # postgres on localhost:5432
# then in cypherd.env:
#   CYPHERD_DATABASE_URL=postgres://cypherpanel:devpassword@localhost:5432/cypherpanel?sslmode=disable
```

---

## First sign-in

Open `https://panel.example.com` (or `http://<vps>:8080` before you add TLS).

- If you left `CYPHERD_ADMIN_EMAIL`/`PASSWORD` **blank**, the panel shows
  **first-run setup** — create the owner account right there in the browser.
- If you **set** them, sign in with those credentials.

Then the panel walks you through the golden path: join your first server (one
copy-paste command), create a project, deploy an app.

## Joining a server

Create the server in the panel and paste the command it gives you onto the host,
as root. It is self-sufficient: it installs Docker if the host has none, fetches
and pins the plane CA (verifying the fingerprint that travels in the command),
enrolls with a single-use join token, and installs a systemd unit that
reconnects across reboots.

The agent binary comes from this project's **latest GitHub release** by default.
When the panel is itself running a release build, its join command pins
`CYPHER_AGENT_URL` to the panel's *own* version, so the server runs the agent
that matches the plane. Override it for an air-gapped fleet or a private mirror:

```sh
curl -fsSL https://panel.example.com/install/agent.sh |   CYPHER_PLANE=… CYPHER_TOKEN=… CYPHER_CA_FINGERPRINT=…   CYPHER_AGENT_URL=https://artifacts.internal/cypher-agent-linux-{arch}   CYPHER_AGENT_SHA256=<sum> sh
```

`{arch}` is replaced with `amd64`/`arm64`. A host that already has
`/usr/local/bin/cypher-agent` reuses it, so a prepared image needs no download.
The panel never stores or serves agent binaries itself (ADR-010) — it only names
a version.

**Certificates renew themselves.** The agent's mTLS identity is a 90-day
certificate (`CYPHERD_AGENT_CERT_TTL`); the agent re-signs it over the same
authenticated channel two thirds of the way through, with a fresh key each time,
and picks up the new material without reconnecting. Nothing to schedule and no
maintenance window. Deleting a server in the panel refuses both its connection
and its renewals, so a decommissioned agent expires rather than lingering. The
agent logs `cert_not_after` at startup if you ever want to see where it stands.

## TLS for applications (Let's Encrypt)

Certificates for your *applications* are obtained by the proxy on each serving
node, not by the panel. The panel owns one ACME account for the whole fleet:

```sh
curl -X PUT https://panel.example.com/api/v1/panel/tls   -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json'   -d '{"acme_email":"ops@example.com"}'
```

Owner-only. Every enrolled node picks it up within a reconcile — no per-host
environment variable, no agent restart — and servers that are offline pick it up
when they return. Add `"acme_ca_server":"https://acme-staging-v02.api.letsencrypt.org/directory"`
while testing so a misconfigured domain does not burn the production rate limit.
Sending an empty `acme_email` clears the account.

**Until you set it, routed applications are served over plain HTTP** and say so:
their `tls_state` reads `http_only_no_resolver` rather than claiming a
certificate that was never issued. That is deliberate — with no ACME account
there is no resolver, and redirecting visitors to `:443` anyway would answer them
with the proxy's self-signed default certificate. `CYPHER_ACME_EMAIL` /
`CYPHER_ACME_CASERVER` on an individual agent still override the panel's account
for that node.

## TLS for the panel

`cypherd` serves the UI/API over plain HTTP on `:8080` — terminate TLS in
front of it. The agent channels (8443/4222) are already mTLS and need no
extra termination. Simplest options:

- **Caddy** (auto HTTPS): `panel.example.com { reverse_proxy localhost:8080 }`
- **A cloud/load-balancer** terminating 443 → 8080.

Point `CYPHERD_PUBLIC_HOST` at the same hostname so the join command the UI
generates is correct, and set **`CYPHERD_PUBLIC_URL`** to the URL a browser
actually types:

```
CYPHERD_PUBLIC_URL=https://panel.example.com
```

It is scheme + host + an optional port and nothing else (a path, query or
unknown scheme is refused at boot). Every link the panel writes to itself is
built from it — the email-change confirmation link, the GitHub webhook URL
shown when an application is created, and the agent join command's installer
fetch and `CYPHER_PLANE_HTTP`. Without it those links carry
`http://<public host>:8080`, which a browser behind TLS cannot follow and
GitHub will not call.

Set **`CYPHERD_TRUSTED_PROXIES`** to the proxy's address or CIDR at the same
time:

```
CYPHERD_TRUSTED_PROXIES=10.0.0.0/8
```

Only from a peer inside that list does the panel read `X-Forwarded-For`,
`X-Real-IP` or an inbound `X-Request-Id`. Left empty behind a proxy, every
client looks like the proxy — so one attacker's failed sign-ins throttle
everybody at that address (the per-account limit still bounds a brute force
against any one account). Set too wide, a client picks its own throttle key.

## Version, update check and diagnostics

`GET /api/v1/panel/version` (any signed-in user) reports the running build and,
when the update check has found one, the newest release beyond it — the same
three stamps `cypherd version` prints. The check polls a release feed every
6 hours, writes **one** inbox item to owners per new version, and never updates
the panel itself (ADR-010):

| Variable | Default | Meaning |
|---|---|---|
| `CYPHERD_UPDATE_CHECK` | `on` | `off` makes no outbound request at all |
| `CYPHERD_UPDATE_FEED_URL` | GitHub releases/latest for this project | The feed to poll |

`GET /api/v1/panel/logs?tail=N` (panel owner, interactive session only, N ≤ 500)
returns the last N lines of cypherd's own log from an in-memory ring — enough
to attach to a bug report without shell access to the host. Every response also
carries an `X-Request-Id`, repeated as `trace_id` in every error body: that is
the value to quote when reporting a fault.

## Upgrades

- **Compose:** `git pull && docker compose up -d --build`.
- **Binary:** rebuild, replace `/usr/local/bin/cypherd`, `systemctl restart
  cypherd`.

Migrations are additive and run automatically on start. Keep the same
`CYPHERD_MASTER_KEY` across upgrades.

### Upgrade every agent in the same window

**There is no agent self-update yet** (ADR-010 is not implemented), and this
release changes the agent↔plane bus contract, so a plane upgraded on its own
leaves a fleet that connects but does nothing. Plan the two together:

1. Upgrade the plane.
2. On **every** server listed under **Servers**, replace the agent binary with
   the build from this release and restart it:

   ```sh
   # on a build machine, from this checkout:
   cd agent && CGO_ENABLED=0 go build -o cypher-agent ./cmd/cypher-agent

   # on each server:
   sudo install -m 0755 ./cypher-agent /usr/local/bin/cypher-agent
   sudo systemctl reset-failed cypher-agent   # only if it already gave up
   sudo systemctl restart cypher-agent
   ```

   The identity in `/var/lib/cypher-agent` is untouched: the server keeps its
   id, certificate and role, and is **not** re-enrolled. Re-running the panel's
   join command does *not* upgrade an agent —
   [install/agent.sh](../../install/agent.sh) reuses the binary already on the
   host unless `CYPHER_AGENT_URL` points at a new one.

**Why.** Reply inboxes on the bus are now scoped per agent identity — an agent
subscribes to `_INBOX_<server-id>.>` and nothing else, so one agent can no
longer read another's desired state (plaintext environment variables) off a
shared `_INBOX.>` wildcard. An older agent still uses the shared prefix, which
the plane no longer grants.

**What it looks like.** On the server, `journalctl -u cypher-agent -f` shows a
NATS permissions violation for a subscription to `_INBOX.…`, then a failure to
bind the work consumer or complete the initial sync, and the process exits.
`systemd` restarts it five times and gives up
(`systemctl status cypher-agent` → `failed`, "start request repeated too
quickly"). In the panel the server flickers green — an out-of-date agent still
heartbeats on each short-lived start — and then goes offline; nothing it is
asked to deploy ever leaves **queued**. The **Servers** list shows each agent's
version, which is how you find the ones still to do.

The plane itself is quiet here: its embedded NATS server does not log
permission refusals. The one plane-side line you may see is a warning naming
the server —

```
bus: dropping desired-state sync whose reply subject is outside the agent's
inbox scope; upgrade cypher-agent on this server to match the plane
  server_id=srv_… reply_subject=_INBOX.…
```

— which appears once per server per plane process, and in
`GET /api/v1/panel/logs`.

**Recovery** is only the reinstall above; nothing is lost and no data migration
is involved. Applications keep running throughout, because an agent that is not
converging still leaves the containers it already started in place.

## Back up

Two things matter: the **Postgres database** (all state) and the
**`CYPHERD_MASTER_KEY`** (decrypts it). Back up the database
(`pg_dump`) and store the master key in a secrets manager. With both, you can
restore the plane anywhere; without the key, sealed secrets are lost.
