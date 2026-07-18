#!/bin/sh
# CypherPanel agent join installer.
#
# Served by the control plane at GET /install/agent.sh and run on the server
# being joined:
#
#   curl -fsSL http://<plane>/install/agent.sh | \
#     CYPHER_PLANE=<host:port> CYPHER_TOKEN=<join-token> \
#     CYPHER_CA_FINGERPRINT=<sha256> sh
#
# The join token is single-use and short-lived by design (threat-model §5.3):
# it appearing in this command line is an accepted, bounded exposure. The CA
# fingerprint travels with the command — over the operator's authenticated
# session — so the CA certificate fetched over the plane's HTTP endpoint is
# verified against out-of-band knowledge before anything trusts it.
#
# Environment:
#   CYPHER_PLANE           gRPC enrollment address, host:port   (required)
#   CYPHER_TOKEN           single-use join token                (required)
#   CYPHER_PLANE_HTTP      plane HTTP base URL; default http://<plane-host>:8080
#   CYPHER_CA_FINGERPRINT  sha256 hex of the plane CA PEM; strongly recommended
#   CYPHER_AGENT_URL       URL of the cypher-agent binary to install; if unset,
#                          an already-installed /usr/local/bin/cypher-agent is
#                          reused (hosted release URLs arrive with the first
#                          public release)
#   CYPHER_AGENT_SHA256    sha256 hex of the binary; verified when set
#   CYPHER_HOSTNAME        hostname to report; default $(hostname)
#   CYPHER_STATE_DIR       agent identity directory; default /var/lib/cypher-agent

set -eu

BIN=/usr/local/bin/cypher-agent
STATE_DIR="${CYPHER_STATE_DIR:-/var/lib/cypher-agent}"

say()  { printf '=> %s\n' "$1"; }
fail() { printf 'error: %s\n' "$1" >&2; exit 1; }

[ "$(id -u)" = 0 ] || fail "run as root (installs to /usr/local/bin and /var/lib)"
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

[ -n "${CYPHER_PLANE:-}" ] || fail "CYPHER_PLANE (control-plane enrollment address, host:port) is required"
[ -n "${CYPHER_TOKEN:-}" ] || fail "CYPHER_TOKEN (join token) is required"

PLANE_HOST=${CYPHER_PLANE%:*}
PLANE_HTTP="${CYPHER_PLANE_HTTP:-http://${PLANE_HOST}:8080}"

# ── agent binary ────────────────────────────────────────────────────────────
if [ -n "${CYPHER_AGENT_URL:-}" ]; then
    say "downloading cypher-agent from $CYPHER_AGENT_URL"
    tmp=$(mktemp)
    curl -fsSL "$CYPHER_AGENT_URL" -o "$tmp"
    if [ -n "${CYPHER_AGENT_SHA256:-}" ]; then
        got=$(sha256sum "$tmp" | cut -d' ' -f1)
        [ "$got" = "$CYPHER_AGENT_SHA256" ] || fail "binary checksum mismatch: got $got"
        say "binary checksum verified"
    fi
    install -m 0755 "$tmp" "$BIN"
    rm -f "$tmp"
elif [ -x "$BIN" ]; then
    say "reusing installed $BIN"
else
    fail "no agent binary: set CYPHER_AGENT_URL (hosted release URLs arrive with CypherPanel's first public release)"
fi

# ── plane CA (pinned for all future traffic — threat-model §5.1) ───────────
ca=$(mktemp)
say "fetching plane CA from $PLANE_HTTP/api/v1/ca.pem"
curl -fsSL "$PLANE_HTTP/api/v1/ca.pem" -o "$ca"
fp=$(sha256sum "$ca" | cut -d' ' -f1)
if [ -n "${CYPHER_CA_FINGERPRINT:-}" ]; then
    [ "$fp" = "$CYPHER_CA_FINGERPRINT" ] || fail "plane CA fingerprint mismatch: got $fp — possible interception, refusing to enroll"
    say "plane CA fingerprint verified"
else
    say "WARNING: CYPHER_CA_FINGERPRINT not set; trusting first fetch (fingerprint: $fp)"
fi

# ── enroll (no-op if this server already holds an identity) ────────────────
if [ -f "$STATE_DIR/identity.json" ]; then
    say "identity already present in $STATE_DIR; skipping enrollment"
else
    say "enrolling with the control plane at $CYPHER_PLANE"
    "$BIN" enroll \
        --plane "$CYPHER_PLANE" \
        --token "$CYPHER_TOKEN" \
        --ca-file "$ca" \
        --state-dir "$STATE_DIR" \
        --hostname "${CYPHER_HOSTNAME:-$(hostname)}"
fi
rm -f "$ca"

# ── run as a service ───────────────────────────────────────────────────────
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    say "installing systemd service cypher-agent"
    cat > /etc/systemd/system/cypher-agent.service <<EOF
[Unit]
Description=CypherPanel agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$BIN run --state-dir $STATE_DIR
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now cypher-agent
    say "agent service started; this server appears in the console within one heartbeat"
else
    say "systemd not detected; start the agent yourself:"
    say "  $BIN run --state-dir $STATE_DIR"
fi
say "done"
