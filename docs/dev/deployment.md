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

## TLS for the panel

`cypherd` serves the UI/API over plain HTTP on `:8080` — terminate TLS in
front of it. The agent channels (8443/4222) are already mTLS and need no
extra termination. Simplest options:

- **Caddy** (auto HTTPS): `panel.example.com { reverse_proxy localhost:8080 }`
- **A cloud/load-balancer** terminating 443 → 8080.

Point `CYPHERD_PUBLIC_HOST` at the same hostname so the join command the UI
generates is correct.

## Upgrades

- **Compose:** `git pull && docker compose up -d --build`.
- **Binary:** rebuild, replace `/usr/local/bin/cypherd`, `systemctl restart
  cypherd`.

Migrations are additive and run automatically on start. Keep the same
`CYPHERD_MASTER_KEY` across upgrades. Agents auto-reconnect; agent
self-update is a separate mechanism (ADR-010), landing with the release
pipeline.

## Back up

Two things matter: the **Postgres database** (all state) and the
**`CYPHERD_MASTER_KEY`** (decrypts it). Back up the database
(`pg_dump`) and store the master key in a secrets manager. With both, you can
restore the plane anywhere; without the key, sealed secrets are lost.
