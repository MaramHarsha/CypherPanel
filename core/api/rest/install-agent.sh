#!/bin/sh
# CypherPanel agent join installer.
#
# Served by the control plane at GET /install/agent.sh and run on the server
# being joined:
#
#   curl -fsSL http://<plane>/install/agent.sh | \
#     CYPHER_PLANE=<host:port> CYPHER_TOKEN=<join-token> \
#     CYPHER_CA_FINGERPRINT=<sha256> CYPHER_AGENT_URL=<binary-url> sh
#
# It brings a fresh Linux VPS to a running, enrolled agent: installs Docker if
# missing (every agent role needs it — build and run both go through the Docker
# engine), installs the agent binary, pins the plane CA, enrolls with the
# single-use token, and starts a systemd service that reconnects across reboots.
#
# The join token is single-use and short-lived by design (threat-model §5.3):
# its appearance on this command line is an accepted, bounded exposure. The CA
# fingerprint travels with the command — over the operator's authenticated
# session — so the CA fetched over plain HTTP is verified against out-of-band
# knowledge before anything trusts it.
#
# The agent binary is fetched from CYPHER_AGENT_URL, an operator-supplied
# artifact URL (ADR-010: the plane names versions but never stores or relays
# agent binaries). Point it at your release asset, object store, or file server.
#
# Environment:
#   CYPHER_PLANE           gRPC enrollment address, host:port          (required)
#   CYPHER_TOKEN           single-use join token                       (required)
#   CYPHER_PLANE_HTTP      plane HTTP base URL; default http://<plane-host>:8080
#   CYPHER_CA_FINGERPRINT  sha256 hex of the plane CA PEM; strongly recommended
#   CYPHER_AGENT_URL       URL of the cypher-agent binary; required on a host
#                          that has no /usr/local/bin/cypher-agent yet. May
#                          contain {arch}, replaced with amd64/arm64.
#   CYPHER_AGENT_SHA256    sha256 hex of the binary; verified when set
#   CYPHER_ROLE            all (default) | builder | worker
#   CYPHER_HOSTNAME        hostname to report; default $(hostname)
#   CYPHER_STATE_DIR       agent identity directory; default /var/lib/cypher-agent
#   CYPHER_SKIP_DOCKER     set to 1 if you manage Docker yourself
#   CYPHER_NO_START        set to 1 to install everything but not start the service

set -eu

BIN=/usr/local/bin/cypher-agent
STATE_DIR="${CYPHER_STATE_DIR:-/var/lib/cypher-agent}"
ROLE="${CYPHER_ROLE:-all}"

say()  { printf '\033[36m=>\033[0m %s\n' "$1"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$1" >&2; }
fail() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

# A single, well-behaved curl: fail on HTTP errors, follow redirects, retry
# transient failures, and never hang forever.
fetch() { curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 15 --max-time 300 "$@"; }

# ── preflight ────────────────────────────────────────────────────────────────
[ "$(id -u)" = 0 ] || fail "run as root (installs to /usr/local/bin, /var/lib, and manages Docker + systemd)"
[ "$(uname -s)" = "Linux" ] || fail "the CypherPanel agent runs on Linux hosts only (found $(uname -s))"
command -v curl >/dev/null 2>&1 || fail "curl is required — install it first (apt-get install -y curl)"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required (coreutils)"

[ -n "${CYPHER_PLANE:-}" ] || fail "CYPHER_PLANE (control-plane enrollment address, host:port) is required"
[ -n "${CYPHER_TOKEN:-}" ] || fail "CYPHER_TOKEN (join token) is required"

case "$ROLE" in
    all|builder|worker) ;;
    *) fail "CYPHER_ROLE must be all, builder or worker (got '$ROLE')" ;;
esac

PLANE_HOST=${CYPHER_PLANE%:*}
PLANE_HTTP="${CYPHER_PLANE_HTTP:-http://${PLANE_HOST}:8080}"

# Normalise the machine architecture to the names release artifacts use.
case "$(uname -m)" in
    x86_64|amd64)   ARCH=amd64 ;;
    aarch64|arm64)  ARCH=arm64 ;;
    *) ARCH=$(uname -m); warn "unrecognised architecture '$ARCH' — the agent binary must match it" ;;
esac

# ── Docker (the agent's runtime: containers + the managed Traefik proxy) ─────
ensure_docker() {
    if [ "${CYPHER_SKIP_DOCKER:-}" = 1 ]; then
        say "CYPHER_SKIP_DOCKER=1 — assuming Docker is managed externally"
        command -v docker >/dev/null 2>&1 || warn "docker not found on PATH; the agent cannot run workloads without it"
        return
    fi
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
        say "Docker present ($(docker version --format '{{.Server.Version}}' 2>/dev/null || echo 'ok'))"
        return
    fi
    if command -v docker >/dev/null 2>&1; then
        say "Docker installed but not running — starting it"
    else
        say "installing Docker (official convenience script from get.docker.com)"
        # Nested curl|sh is the vendor-supported install path; it handles every
        # major distro's package manager.
        if ! fetch https://get.docker.com | sh; then
            fail "Docker install failed. Install Docker manually (https://docs.docker.com/engine/install/) then re-run, or set CYPHER_SKIP_DOCKER=1"
        fi
    fi
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
        systemctl enable --now docker >/dev/null 2>&1 || true
    fi
    # Wait for the daemon socket to answer — get.docker.com starts it, but not
    # always before we return.
    i=0
    while [ "$i" -lt 30 ]; do
        docker info >/dev/null 2>&1 && break
        i=$((i + 1))
        sleep 1
    done
    docker info >/dev/null 2>&1 || fail "Docker is installed but the daemon is not responding (checked 'docker info' for 30s)"
    say "Docker is running"
}
ensure_docker

# The proxy binds :80 and :443 on hosts that run apps (all/worker). A conflict
# is worth surfacing now, not as a silent Traefik crash-loop later.
if [ "$ROLE" != "builder" ] && command -v ss >/dev/null 2>&1; then
    for port in 80 443; do
        if ss -ltnH "( sport = :$port )" 2>/dev/null | grep -q .; then
            warn "port $port is already in use — the CypherPanel proxy needs it; free it or that app's routing will fail"
        fi
    done
fi

# Certificate validity is time-sensitive; a badly-skewed clock breaks mTLS.
if command -v timedatectl >/dev/null 2>&1; then
    timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes \
        || warn "system clock may not be NTP-synced — enrollment can fail on large clock skew (timedatectl set-ntp true)"
fi

# ── agent binary ─────────────────────────────────────────────────────────────
if [ -n "${CYPHER_AGENT_URL:-}" ]; then
    url=$(printf '%s' "$CYPHER_AGENT_URL" | sed "s/{arch}/$ARCH/g")
    say "downloading cypher-agent ($ARCH) from $url"
    tmp=$(mktemp)
    fetch "$url" -o "$tmp" || fail "could not download the agent binary from $url"
    if [ -n "${CYPHER_AGENT_SHA256:-}" ]; then
        got=$(sha256sum "$tmp" | cut -d' ' -f1)
        [ "$got" = "$CYPHER_AGENT_SHA256" ] || fail "binary checksum mismatch (got $got) — refusing to install"
        say "binary checksum verified"
    fi
    # Sanity-check we downloaded an executable, not an HTML error page.
    head -c 4 "$tmp" | grep -q 'ELF' || fail "downloaded file is not a Linux binary (is $url correct?)"
    install -m 0755 "$tmp" "$BIN"
    rm -f "$tmp"
    say "installed $("$BIN" version 2>/dev/null || echo cypher-agent) to $BIN"
elif [ -x "$BIN" ]; then
    say "reusing installed $BIN ($("$BIN" version 2>/dev/null || echo 'unknown version'))"
else
    fail "no agent binary found. Set CYPHER_AGENT_URL to the cypher-agent binary for linux/$ARCH (ADR-010: the plane does not host binaries — point it at your release asset or file server)"
fi

# ── plane CA (pinned for all future traffic — threat-model §5.1) ─────────────
ca=$(mktemp)
say "fetching plane CA from $PLANE_HTTP/api/v1/ca.pem"
fetch "$PLANE_HTTP/api/v1/ca.pem" -o "$ca" || fail "could not fetch the plane CA from $PLANE_HTTP — is CYPHER_PLANE_HTTP reachable from this host?"
fp=$(sha256sum "$ca" | cut -d' ' -f1)
if [ -n "${CYPHER_CA_FINGERPRINT:-}" ]; then
    [ "$fp" = "$CYPHER_CA_FINGERPRINT" ] || fail "plane CA fingerprint mismatch (got $fp) — possible interception, refusing to enroll"
    say "plane CA fingerprint verified"
else
    warn "CYPHER_CA_FINGERPRINT not set; trusting first fetch (fingerprint: $fp)"
fi

# ── enroll (idempotent: a host that already holds an identity keeps it) ──────
if [ -f "$STATE_DIR/identity.json" ]; then
    say "identity already present in $STATE_DIR; skipping enrollment"
else
    say "enrolling with the control plane at $CYPHER_PLANE"
    "$BIN" enroll \
        --plane "$CYPHER_PLANE" \
        --token "$CYPHER_TOKEN" \
        --ca-file "$ca" \
        --state-dir "$STATE_DIR" \
        --hostname "${CYPHER_HOSTNAME:-$(hostname)}" \
        || fail "enrollment failed — the join token may be expired or already used (generate a new one in the panel)"
fi
rm -f "$ca"

# ── run as a service ─────────────────────────────────────────────────────────
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    say "installing systemd service cypher-agent (role: $ROLE)"
    cat > /etc/systemd/system/cypher-agent.service <<EOF
[Unit]
Description=CypherPanel agent
Documentation=https://github.com/MaramHarsha/cypherpanel
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
# Don't let a hard crash loop hammer the box or the plane (systemd v230+ reads
# these from [Unit]); back off if it fails repeatedly, visible in the unit state.
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
ExecStart=$BIN run --state-dir $STATE_DIR --role $ROLE
Restart=always
RestartSec=5
# The agent manages the Docker socket and binds :80/:443 via Traefik, so it
# needs root; keep only the sandboxing that doesn't fight that.
NoNewPrivileges=false
ProtectHome=true
ProtectSystem=false

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    if [ "${CYPHER_NO_START:-}" = 1 ]; then
        systemctl enable cypher-agent >/dev/null 2>&1 || true
        say "service installed but not started (CYPHER_NO_START=1). Start it with: systemctl start cypher-agent"
    else
        systemctl enable --now cypher-agent
        # Give it a moment, then report the real unit state rather than assume.
        sleep 2
        if systemctl is-active --quiet cypher-agent; then
            say "agent service is running — this server appears in the panel within one heartbeat (~30s)"
        else
            warn "the agent service did not stay active. Check: journalctl -u cypher-agent -n 50 --no-pager"
        fi
    fi
else
    say "systemd not detected; start the agent yourself:"
    say "  $BIN run --state-dir $STATE_DIR --role $ROLE"
fi
say "done"
