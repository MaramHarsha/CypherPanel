#!/usr/bin/env bash
# A fresh Linux host, installed the way the README says — in a throwaway
# systemd container with its OWN Docker daemon, so nothing here can touch a
# panel already running on this machine.
#
# What it proves, in order: install.sh on a bare host (Docker from
# get.docker.com, PostgreSQL, the master key, the systemd units), the first-run
# owner account, a re-run that converges without regenerating the master key, a
# reboot the panel survives, "use this machine" installing an agent through the
# root helper, a template deployed through the real agent and served through the
# real Proxy at its domain, and a second reboot with that workload on it.
#
# scripts/release-rehearsal.sh covers the plane on its own (build, migrate,
# snapshot, restore). This covers the HOST — the part no unit test and no CI job
# running inside a prepared runner can see.
#
#   make fresh-host-rehearsal
#   FRESH_KEEP=1 …          leave the container running afterwards
#   REHEARSAL_GO=…          the Go wrapper, as for release-rehearsal.sh
#
# The panel is stamped `dev` on purpose: a release-versioned panel has "use this
# machine" fetch its agent from that release's GitHub asset, which only exists
# once the release is PUBLISHED (a draft is not downloadable). A dev panel reuses
# the agent binary already on the host, which is agent.sh's documented
# prepared-out-of-band case and the one a rehearsal can exercise before a tag.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$ROOT/.rehearsal/fresh"
NAME=cypher-fresh-host
IMAGE=cypher-fresh-host:1
API=http://127.0.0.1:8080
RUN_GO="${REHEARSAL_GO:-sh -c}"
GO_PIN="GOTOOLCHAIN=go$(awk '/^go /{print $2; exit}' "$ROOT/go.work")"
TEMPLATE="${FRESH_TEMPLATE:-uptime-kuma}"

PASS=0; FAILED=0
say()  { printf '\n\033[36m== %s\033[0m\n' "$1"; }
ok()   { PASS=$((PASS+1)); printf '\033[32m  ok\033[0m %s\n' "$1"; }
bad()  { FAILED=$((FAILED+1)); printf '\033[31m  NO\033[0m %s\n' "$1"; }
fail() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }
x()    { docker exec "$NAME" "$@"; }
cleanup() {
    if [ "${FRESH_KEEP:-}" = 1 ]; then
        say "FRESH_KEEP=1 — $NAME left running (docker rm -f $NAME; docker volume rm cypher-fresh-docker cypher-fresh-containerd)"
        return
    fi
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    docker volume rm cypher-fresh-docker cypher-fresh-containerd >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$WORK"
command -v docker >/dev/null || fail "docker is required"

# ── binaries: a dev-stamped plane, and the agent ─────────────────────────────
say "building a dev-stamped cypherd and cypher-agent"
test -f "$ROOT/core/api/rest/webui/dist/index.html" || fail "core/api/rest/webui/dist is missing — run 'make build-web'"
$RUN_GO "cd '$ROOT/core' && $GO_PIN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version=dev' -o '$WORK/cypherd' ./cmd/cypherd" || fail "building cypherd"
$RUN_GO "cd '$ROOT/agent' && $GO_PIN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w -X main.version=dev' -o '$WORK/cypher-agent' ./cmd/cypher-agent" || fail "building cypher-agent"
ok "built"

# ── a bare host ──────────────────────────────────────────────────────────────
say "booting a bare systemd host"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    cat > "$WORK/Dockerfile" <<'EOF'
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv curl ca-certificates iproute2 dbus && rm -rf /var/lib/apt/lists/* \
 && systemctl mask systemd-udevd.service systemd-udevd-kernel.socket systemd-udevd-control.socket systemd-networkd-wait-online.service 2>/dev/null || true
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
EOF
    docker build -q -t "$IMAGE" "$WORK" >/dev/null || fail "building the host image"
fi
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker volume rm cypher-fresh-docker cypher-fresh-containerd >/dev/null 2>&1 || true
# Docker-in-Docker needs its state on a real filesystem: overlay on overlay is
# refused by the kernel, and containerd's root is what the first run tripped on.
docker run -d --privileged --cgroupns=private --name "$NAME" \
    --tmpfs /run --tmpfs /run/lock --tmpfs /tmp \
    -v cypher-fresh-docker:/var/lib/docker -v cypher-fresh-containerd:/var/lib/containerd \
    "$IMAGE" >/dev/null || fail "the host container did not start"
wait_systemd() {
    for i in $(seq 120); do
        st=$(x systemctl is-system-running 2>/dev/null)
        case "$st" in running|degraded) return 0 ;; esac
        sleep 1
    done
    return 1
}
wait_systemd && ok "systemd is up" || fail "systemd never came up"
docker cp "$WORK/cypherd" "$NAME:/opt/cypherd"
docker cp "$ROOT/install/install.sh" "$NAME:/opt/install.sh"

# ── install.sh, exactly as documented ────────────────────────────────────────
say "install.sh (binary from a file:// URL — the documented path before a release exists)"
if docker exec -e CYPHERD_URL=file:///opt/cypherd -e CYPHERD_PUBLIC_HOST=127.0.0.1 "$NAME" sh /opt/install.sh > "$WORK/install.log" 2>&1; then
    ok "install.sh exited 0"
else
    bad "install.sh failed"; tail -20 "$WORK/install.log"; x journalctl -u cypherd -n 30 --no-pager; exit 1
fi
for u in cypherd cypherd-upgrade.path cypherd-localjoin.path; do
    x systemctl is-active "$u" >/dev/null && ok "$u is active" || bad "$u is not active"
done
x curl -sf "$API/readyz" >/dev/null && ok "/readyz answers" || bad "/readyz does not answer"
x stat -c '%a %U' /etc/cypherpanel/cypherd.env | grep -q '^600 root' && ok "cypherd.env is 0600 root" || bad "cypherd.env permissions"
KEY1=$(x sed -n 's/^CYPHERD_MASTER_KEY=//p' /etc/cypherpanel/cypherd.env)

say "first-run owner account (with the setup code the installer wrote)"
SETUP_TOKEN=$(x sed -n 's/^CYPHERD_SETUP_TOKEN=//p' /etc/cypherpanel/cypherd.env)
[ -n "$SETUP_TOKEN" ] && ok "the installer generated a setup code" || bad "no setup code in cypherd.env"
x curl -s -o /dev/null -w '%{http_code}' -X POST "$API/api/v1/auth/setup" -H 'Content-Type: application/json' \
    -d '{"email":"intruder@example.com","password":"intruder-password-1","setup_token":"wrong"}' | grep -q '^403$' \
    && ok "a claim without the code is refused (403)" || bad "a claim without the code was NOT refused"
x curl -sf -X POST "$API/api/v1/auth/setup" -H 'Content-Type: application/json' \
    -d "{\"email\":\"owner@example.com\",\"password\":\"owner-password-1\",\"setup_token\":\"$SETUP_TOKEN\"}" >/dev/null 2>&1 || true
login() {
    x curl -sf -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
        -d '{"email":"owner@example.com","password":"owner-password-1"}' | sed 's/.*"token":"\([^"]*\)".*/\1/'
}
TOKEN=$(login); [ -n "$TOKEN" ] && ok "owner created and signed in" || bad "could not create the owner"
x curl -sf -X POST "$API/api/v1/projects" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"name":"fresh"}' | grep -q '"id"' && ok "a project was created" || bad "could not create a project"

say "re-running install.sh (must converge; must NOT regenerate the master key)"
docker exec -e CYPHERD_URL=file:///opt/cypherd -e CYPHERD_PUBLIC_HOST=127.0.0.1 "$NAME" sh /opt/install.sh >/dev/null 2>&1 \
    && ok "re-run exited 0" || bad "re-run failed"
KEY2=$(x sed -n 's/^CYPHERD_MASTER_KEY=//p' /etc/cypherpanel/cypherd.env)
[ "$KEY1" = "$KEY2" ] && ok "master key preserved" || bad "MASTER KEY CHANGED — every sealed secret is now unreadable"

say "rebooting the host"
docker restart "$NAME" >/dev/null
wait_systemd || bad "systemd did not come back"
for i in $(seq 90); do x curl -sf "$API/readyz" >/dev/null 2>&1 && break; sleep 1; done
x curl -sf "$API/readyz" >/dev/null && ok "the panel came back by itself (${i}s)" || { bad "the panel did not come back"; x journalctl -u cypherd -n 30 --no-pager; }
TOKEN=$(login)
x curl -sf "$API/api/v1/projects" -H "Authorization: Bearer $TOKEN" | grep -q fresh && ok "the project survived the reboot" || bad "the project did not survive"

# ── use this machine ─────────────────────────────────────────────────────────
say "use this machine (the root helper installs the agent already on the host)"
docker cp "$WORK/cypher-agent" "$NAME:/usr/local/bin/cypher-agent"; x chmod 0755 /usr/local/bin/cypher-agent
x curl -sf "$API/api/v1/servers/local" -H "Authorization: Bearer $TOKEN" | grep -q '"state":"available"' \
    && ok "the panel offers it" || bad "GET /servers/local is not 'available': $(x curl -s "$API/api/v1/servers/local" -H "Authorization: Bearer $TOKEN")"
RESP=$(x curl -s -X POST "$API/api/v1/servers/local" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}')
SRV=$(printf '%s' "$RESP" | sed -n 's/.*"server":{"id":"\([^"]*\)".*/\1/p')
[ -n "$SRV" ] && ok "request accepted" || { bad "POST /servers/local: $RESP"; exit 1; }
for i in $(seq 180); do
    x curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" | grep -q '"status":"running"' && break; sleep 1
done
if x curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" | grep -q '"status":"running"'; then
    ok "the agent installed itself, enrolled and is running (${i}s)"
else
    bad "the server never came running: $(x cat /var/lib/cypherpanel/upgrade/localjoin-status.json 2>/dev/null | tr -d '\n' | cut -c1-300)"
    x journalctl -u cypherd-localjoin -n 20 --no-pager; exit 1
fi
x systemctl is-active cypher-agent >/dev/null && ok "cypher-agent.service is active" || bad "cypher-agent.service is not active"

# ── a real deploy, served at a domain ────────────────────────────────────────
say "deploying the $TEMPLATE template at a domain"
PJ=$(x curl -sf -X POST "$API/api/v1/projects" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"fresh-deploy"}')
ENV=$(printf '%s' "$PJ" | sed -n 's/.*"default_environment":{"id":"\([^"]*\)".*/\1/p')
INST=$(x curl -s -X POST "$API/api/v1/templates/$TEMPLATE/install" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"environment_id\":\"$ENV\",\"server_id\":\"$SRV\",\"domain\":\"app.example.test\",\"name\":\"app\"}")
APP=$(printf '%s' "$INST" | sed -n 's/.*"applications":\["\([^"]*\)".*/\1/p')
[ -n "$APP" ] && ok "installed as application $APP" || { bad "install: $INST"; exit 1; }
for i in $(seq 420); do
    ST=$(x curl -sf "$API/api/v1/applications/$APP" -H "Authorization: Bearer $TOKEN" | sed -n 's/.*"status":"\([a-z_]*\)".*/\1/p')
    case "$ST" in running|error) break ;; esac; sleep 1
done
[ "$ST" = running ] && ok "running after ${i}s (pull, health gate, route)" || { bad "application status is '$ST'"; x sh -c 'docker ps -a --format "{{.Names}} {{.Status}}"'; exit 1; }
served() { x curl -s -o /dev/null -w '%{http_code}' -H 'Host: app.example.test' http://127.0.0.1/ 2>/dev/null; }
for i in $(seq 30); do CODE=$(served); case "$CODE" in 200|30[0-9]) break ;; esac; sleep 1; done
case "$CODE" in 200|30[0-9]) ok "served through the Proxy at http://app.example.test/ (HTTP $CODE)" ;; *) bad "the Proxy answered HTTP $CODE"; x sh -c 'docker logs cypher-proxy 2>&1 | tail -10' ;; esac

say "rebooting the host with a workload on it"
docker restart "$NAME" >/dev/null
wait_systemd || bad "systemd did not come back"
for i in $(seq 180); do CODE=$(served); case "$CODE" in 200|30[0-9]) break ;; esac; sleep 1; done
case "$CODE" in 200|30[0-9]) ok "the app is served again by itself, ${i}s after boot (HTTP $CODE)" ;; *) bad "the app is NOT served after the reboot (HTTP $CODE)"; x sh -c 'docker ps -a --format "{{.Names}} {{.Status}}"' ;; esac
TOKEN=$(login)
x curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" | grep -q '"status":"running"' && ok "the server is running in the panel" || bad "the server is not running in the panel"

say "result"
printf '  %s checks passed, %s failed\n\n' "$PASS" "$FAILED"
[ "$FAILED" -eq 0 ]
