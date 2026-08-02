#!/bin/sh
# CypherPanel — one-command install for the control plane.
#
#   curl -fsSL https://raw.githubusercontent.com/MaramHarsha/CypherPanel/main/install/install.sh | sh
#
# Brings a fresh Linux VPS to a running panel: Docker, PostgreSQL, the cypherd
# binary, a generated master key, and a systemd unit that survives reboots.
# When it finishes you open the panel in a browser and create the owner account
# there (first-run-setup.md) — no password is ever printed or defaulted.
#
# Re-running is safe. It converges: an existing master key is NEVER regenerated
# (that would make every sealed secret — env vars, deploy keys, the agent CA —
# permanently unreadable), an existing database is left alone, and only the
# binary and unit are refreshed.
#
# Environment:
#   CYPHERD_URL        binary URL; {arch} is replaced with amd64/arm64.
#                      Defaults to the latest GitHub release asset.
#   CYPHERD_SHA256     sha256 of the binary; verified when set.
#   CYPHERD_PUBLIC_HOST  hostname/IP agents and browsers reach this plane at.
#                      Auto-detected when unset.
#   CYPHERD_HTTP_PORT  panel port (default 8080).
#   CYPHER_SKIP_DOCKER set to 1 if you manage Docker yourself.
#   POSTGRES_IMAGE     default postgres:16-alpine.
#
# The agent is installed separately, per server, from the panel's own join
# command (install/agent.sh) — this script sets up the plane only.
set -eu

REPO="MaramHarsha/CypherPanel"
ETC_DIR="/etc/cypherpanel"
ENV_FILE="$ETC_DIR/cypherd.env"
BIN="/usr/local/bin/cypherd"
UNIT="/etc/systemd/system/cypherd.service"
PG_NAME="cypherpanel-postgres"
PG_IMAGE="${POSTGRES_IMAGE:-postgres:16-alpine}"
HTTP_PORT="${CYPHERD_HTTP_PORT:-8080}"
ENROLL_PORT=8443
NATS_PORT=4222

say()  { printf '\033[36m=>\033[0m %s\n' "$1"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$1" >&2; }
ok()   { printf '\033[32m  ok\033[0m %s\n' "$1"; }
fail() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }
fetch() { curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 15 --max-time 600 "$@"; }

# ── preflight ────────────────────────────────────────────────────────────────

[ "$(id -u)" = 0 ] || fail "run as root (try: sudo sh -c \"\$(curl -fsSL .../install.sh)\")"
[ "$(uname -s)" = "Linux" ] || fail "CypherPanel's control plane runs on Linux; found $(uname -s)"
command -v curl >/dev/null 2>&1 || fail "curl is required"

case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) fail "unsupported architecture $(uname -m) — amd64 and arm64 are built" ;;
esac

[ -d /run/systemd/system ] || fail "systemd is required (this installs a systemd service)"

# Free disk matters: Postgres, images, and build contexts all land on /.
avail_mb=$(df -Pm / | awk 'NR==2 {print $4}')
[ "${avail_mb:-0}" -ge 2048 ] || fail "at least 2 GB free on / is required, found ${avail_mb} MB"

# A port already in use is the failure that otherwise shows up much later as an
# unexplained crash loop, so name it now.
port_busy() {
    command -v ss >/dev/null 2>&1 || return 1
    ss -ltnH "( sport = :$1 )" 2>/dev/null | grep -q .
}
for p in "$HTTP_PORT" "$ENROLL_PORT" "$NATS_PORT"; do
    if port_busy "$p"; then
        # Our own service holding it is exactly what a re-run looks like.
        if systemctl is-active --quiet cypherd 2>/dev/null; then continue; fi
        fail "port $p is already in use by something else — free it, or set CYPHERD_HTTP_PORT"
    fi
done
for p in 80 443; do
    if port_busy "$p"; then
        warn "port $p is in use. Apps are published through a Traefik proxy that needs 80 and 443 on whichever server runs them. If that is this machine, free them or you will not be able to serve any app at a domain."
    fi
done

say "CypherPanel install — linux/$ARCH"

# ── docker ───────────────────────────────────────────────────────────────────

ensure_docker() {
    if [ "${CYPHER_SKIP_DOCKER:-}" = 1 ]; then
        say "CYPHER_SKIP_DOCKER=1 — assuming Docker is managed externally"
        return
    fi
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
        ok "Docker present"
        return
    fi
    if ! command -v docker >/dev/null 2>&1; then
        say "installing Docker"
        fetch https://get.docker.com | sh >/dev/null 2>&1 \
            || fail "Docker install failed — install it manually (https://docs.docker.com/engine/install/) and re-run"
    fi
    systemctl enable --now docker >/dev/null 2>&1 || true
    i=0
    while [ "$i" -lt 30 ]; do
        docker info >/dev/null 2>&1 && break
        i=$((i + 1)); sleep 1
    done
    docker info >/dev/null 2>&1 || fail "Docker is installed but its daemon is not responding"
    ok "Docker running"
}
ensure_docker

# ── secrets: generated once, never regenerated ───────────────────────────────

mkdir -p "$ETC_DIR"
chmod 700 "$ETC_DIR"

rand() { head -c "$1" /dev/urandom | base64 | tr -d '\n/+=' | cut -c "1-$2"; }

read_env() {
    [ -f "$ENV_FILE" ] || return 0
    sed -n "s/^$1=//p" "$ENV_FILE" | head -1
}

MASTER_KEY="$(read_env CYPHERD_MASTER_KEY)"
PG_PASSWORD="$(read_env POSTGRES_PASSWORD)"

if [ -n "$MASTER_KEY" ]; then
    # Reusing it is not an optimisation — regenerating would orphan every
    # sealed secret already in the database, including the agent CA key, and
    # nothing would be able to decrypt them again.
    ok "existing master key preserved"
else
    MASTER_KEY="$(head -c 32 /dev/urandom | base64)"
    say "generated a new master key"
fi
[ -n "$PG_PASSWORD" ] || PG_PASSWORD="$(rand 48 32)"

# ── public host ──────────────────────────────────────────────────────────────

PUBLIC_HOST="${CYPHERD_PUBLIC_HOST:-$(read_env CYPHERD_PUBLIC_HOST)}"
if [ -z "$PUBLIC_HOST" ]; then
    # Agents dial this address and it lands in the plane's certificate SANs, so
    # a wrong guess breaks enrollment — prefer the public IP over a hostname
    # that may only resolve on the LAN.
    PUBLIC_HOST="$(fetch https://api.ipify.org 2>/dev/null || true)"
    [ -n "$PUBLIC_HOST" ] || PUBLIC_HOST="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [ -n "$PUBLIC_HOST" ] || fail "could not detect this machine's address — set CYPHERD_PUBLIC_HOST"
fi
ok "public host: $PUBLIC_HOST"

# ── postgres ─────────────────────────────────────────────────────────────────

if docker inspect "$PG_NAME" >/dev/null 2>&1; then
    docker start "$PG_NAME" >/dev/null 2>&1 || true
    ok "PostgreSQL container already exists — left as is"
else
    say "starting PostgreSQL ($PG_IMAGE)"
    # Bound to loopback: the database is never a public service. The panel and
    # the database live on the same host by design (ADR-001).
    docker run -d --name "$PG_NAME" \
        --restart unless-stopped \
        -e POSTGRES_USER=cypherpanel \
        -e POSTGRES_PASSWORD="$PG_PASSWORD" \
        -e POSTGRES_DB=cypherpanel \
        -p 127.0.0.1:5432:5432 \
        -v cypherpanel-pgdata:/var/lib/postgresql/data \
        "$PG_IMAGE" >/dev/null || fail "could not start PostgreSQL"
fi

say "waiting for PostgreSQL"
i=0
while [ "$i" -lt 60 ]; do
    docker exec "$PG_NAME" pg_isready -U cypherpanel -d cypherpanel >/dev/null 2>&1 && break
    i=$((i + 1)); sleep 1
done
docker exec "$PG_NAME" pg_isready -U cypherpanel -d cypherpanel >/dev/null 2>&1 \
    || fail "PostgreSQL did not become ready within 60s (docker logs $PG_NAME)"
ok "PostgreSQL ready"

# ── binary ───────────────────────────────────────────────────────────────────

URL="${CYPHERD_URL:-https://github.com/$REPO/releases/latest/download/cypherd-linux-{arch}}"
URL="$(printf '%s' "$URL" | sed "s/{arch}/$ARCH/g")"

TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT INT TERM

say "downloading cypherd"
if ! fetch -o "$TMP/cypherd" "$URL"; then
    fail "could not download cypherd from $URL
  No release published yet? Build it from a checkout instead:
      git clone https://github.com/$REPO && cd CypherPanel/core
      go build -o /usr/local/bin/cypherd ./cmd/cypherd
  then re-run this script with CYPHERD_URL=file:///usr/local/bin/cypherd"
fi

if [ -n "${CYPHERD_SHA256:-}" ]; then
    command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required to verify CYPHERD_SHA256"
    got="$(sha256sum "$TMP/cypherd" | awk '{print $1}')"
    [ "$got" = "$CYPHERD_SHA256" ] || fail "checksum mismatch: expected $CYPHERD_SHA256, got $got"
    ok "checksum verified"
fi

chmod 0755 "$TMP/cypherd"
"$TMP/cypherd" --version >/dev/null 2>&1 || true
install -m 0755 "$TMP/cypherd" "$BIN"
ok "installed $BIN"

# ── config ───────────────────────────────────────────────────────────────────

# Written whole each run so upgrades pick up new defaults, with the two values
# that must never change carried over from above.
umask 077
cat > "$ENV_FILE" <<EOF
# CypherPanel control plane. Generated by install.sh — keep this file secret.
#
# CYPHERD_MASTER_KEY decrypts every sealed secret: env vars, deploy keys,
# backup credentials, and the agent CA key. Back it up. If you lose it, those
# secrets are unrecoverable; if you change it, they stop decrypting.
POSTGRES_PASSWORD=$PG_PASSWORD
CYPHERD_MASTER_KEY=$MASTER_KEY
CYPHERD_DATABASE_URL=postgres://cypherpanel:$PG_PASSWORD@127.0.0.1:5432/cypherpanel?sslmode=disable
CYPHERD_PUBLIC_HOST=$PUBLIC_HOST
CYPHERD_HTTP_ADDR=0.0.0.0:$HTTP_PORT
CYPHERD_ENROLL_ADDR=0.0.0.0:$ENROLL_PORT
CYPHERD_NATS_ADDR=0.0.0.0:$NATS_PORT

# Left blank on purpose: the owner account is created in the browser on first
# visit, so no password is ever written to disk or printed to a terminal.
CYPHERD_ADMIN_EMAIL=
CYPHERD_ADMIN_PASSWORD=
EOF
chmod 600 "$ENV_FILE"
ok "wrote $ENV_FILE (0600)"

# ── service ──────────────────────────────────────────────────────────────────

cat > "$UNIT" <<'EOF'
[Unit]
Description=CypherPanel control plane
Documentation=https://github.com/MaramHarsha/CypherPanel
After=network-online.target docker.service
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
EnvironmentFile=/etc/cypherpanel/cypherd.env
ExecStart=/usr/local/bin/cypherd
Restart=always
RestartSec=5

StateDirectory=cypherd
Environment=CYPHERD_DATA_DIR=/var/lib/cypherd

# cypherd is a network service with a data directory and needs nothing else.
DynamicUser=true
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable cypherd >/dev/null 2>&1
systemctl restart cypherd
say "waiting for the panel"

i=0
while [ "$i" -lt 60 ]; do
    if curl -fsS -m 2 "http://127.0.0.1:$HTTP_PORT/readyz" >/dev/null 2>&1; then break; fi
    i=$((i + 1)); sleep 1
done
if ! curl -fsS -m 2 "http://127.0.0.1:$HTTP_PORT/readyz" >/dev/null 2>&1; then
    fail "the panel did not become ready within 60s. Logs:
      journalctl -u cypherd -n 50 --no-pager"
fi
ok "panel is running"

# ── done ─────────────────────────────────────────────────────────────────────

printf '\n\033[32mCypherPanel is installed.\033[0m\n\n'
printf '  Open   http://%s:%s\n' "$PUBLIC_HOST" "$HTTP_PORT"
printf '  and create the owner account — that screen appears exactly once.\n\n'
printf '  Anyone who reaches the panel before you can claim it, so do this now,\n'
printf '  or restrict the port until you have:\n'
printf '      ufw allow from YOUR.IP.ADDRESS to any port %s proto tcp\n\n' "$HTTP_PORT"
printf '  Back up your master key — sealed secrets cannot be recovered without it:\n'
printf '      %s\n\n' "$ENV_FILE"
printf '  Add a server from the panel, then paste its join command on that host.\n'
printf '  Service:  systemctl status cypherd   ·   journalctl -u cypherd -f\n\n'
