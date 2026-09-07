#!/usr/bin/env bash
# Browser regression tests against a real panel (web/e2e/README.md).
#
# Boots a throwaway PostgreSQL, a real cypherd with the built UI embedded, and
# a real enrolled agent — then drives the panel through a real browser. Nothing
# is mocked, because the defects this exists to catch are precisely the ones
# that live between a working API and a missing control.
#
# Everything it creates is namespaced `cypher-e2e-*` and removed on exit.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${E2E_WORK_DIR:-$ROOT/.e2e}"
PG_NAME=cypher-e2e-pg
PG_PORT="${E2E_PG_PORT:-15441}"
# Off the defaults so this never collides with a panel running on the same box.
HTTP_PORT="${E2E_HTTP_PORT:-8099}"
ENROLL_PORT="${E2E_ENROLL_PORT:-8449}"
NATS_PORT="${E2E_NATS_PORT:-4229}"
PLAYWRIGHT_IMAGE="${E2E_PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright:v1.49.0-noble}"
API="http://127.0.0.1:$HTTP_PORT"

EMAIL=e2e@example.com
PASSWORD=e2e-password-1

say() { printf '\033[36m=>\033[0m %s\n' "$1"; }
fail() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

cleanup() {
    # E2E_KEEP means KEEP: killing the panel and then announcing it was left
    # running is worse than not offering the option, and it cost a debugging
    # round when this script did exactly that.
    if [ "${E2E_KEEP:-}" != 1 ]; then
        [ -f "$WORK/cypherd.pid" ] && kill "$(cat "$WORK/cypherd.pid")" 2>/dev/null || true
        [ -f "$WORK/agent.pid" ] && kill "$(cat "$WORK/agent.pid")" 2>/dev/null || true
    fi
    if [ "${E2E_KEEP:-}" != 1 ]; then
        docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
        # The RUN state only. Deleting all of $WORK would also delete binaries
        # an E2E_SKIP_BUILD=1 caller put there, which is their input, not ours.
        rm -rf "$WORK/data" "$WORK/agent" "$WORK/ca.pem" "$WORK/cypherd.log" \
               "$WORK/agent.log" "$WORK/cypherd.pid" "$WORK/agent.pid"
        [ "${E2E_SKIP_BUILD:-}" = 1 ] || rm -rf "$WORK"
    else
        say "E2E_KEEP=1 — panel left running on $API (docker rm -f $PG_NAME to finish)"
    fi
}
trap cleanup EXIT

# REFUSE TO RUN BESIDE A LIVE AGENT.
#
# The Proxy's container name (`cypher-proxy`) is a HOST-GLOBAL constant, not a
# per-agent one (agent/proxy/ensure.go). So a second agent on the same host
# converges that same container and re-points it at its own Traefik directory,
# taking over routing for whatever the first agent was serving until the first
# one converges it back. On a laptop that is a puzzle; on a machine running a
# real panel it is an outage — and this suite ran on exactly such a machine
# once before the check existed.
#
# A suite that exists to catch defects must not cause them.
# E2E_NO_AGENT=1 boots the panel alone. The contention this guard exists for is
# between AGENTS — two of them converge the same `cypher-proxy` container — so a
# run that starts none cannot disturb anything, and the specs that need no
# enrolled server can be debugged on a host that has one.
if [ "${E2E_NO_AGENT:-}" != 1 ] \
    && [ "${E2E_I_KNOW_THIS_HOST_HAS_NO_AGENT:-}" != 1 ] \
    && command -v docker >/dev/null 2>&1 \
    && docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^cypher-proxy$'; then
    fail "a CypherPanel proxy is already running on this host, so an agent is too.
  Two agents on one host fight over the container named 'cypher-proxy' and over
  Traefik's configuration directory, so running this suite here would disturb
  whatever that agent is serving. Run it on a host with no agent, or in CI.
  E2E_I_KNOW_THIS_HOST_HAS_NO_AGENT=1 overrides this, and you should be sure."
fi

# A panel already answering on this port is one THIS RUN did not start, almost
# always an E2E_KEEP=1 leftover. Reusing it silently means testing an older
# binary and believing the result — which cost two debugging rounds before this
# check existed, both spent on a fix that was already correct.
if curl -sf "$API/readyz" >/dev/null 2>&1; then
    fail "something is already serving $API — probably a panel left by E2E_KEEP=1.
  This run would test THAT binary, not the one you just built.
  Stop it first:  pkill -f '$WORK/cypherd'; docker rm -f $PG_NAME"
fi

mkdir -p "$WORK"

# wait_for polls a command until it succeeds, then reports honestly if it never
# does. Written as `if ... then ... fi` rather than `cmd && break` on purpose:
# under `set -e` a failing `&&` list inside a loop body aborts the whole script,
# which is how the first version of this managed to die in under two seconds
# while claiming it had waited forty.
# STREAK is how many consecutive successes count as "up". One is not enough for
# anything that restarts while starting: the postgres image runs initdb against
# a TEMPORARY server, which answers, and then shuts it down for the real one. A
# probe that breaks on the first yes can therefore return during a window that
# closes a moment later — which is how this job died in CI 1.4 seconds into a
# wait meant to last forty, on a runner slow enough to land inside it. The race
# does not reproduce on a fast machine, so requiring a streak is the fix that
# does not depend on being able to reproduce it.
wait_for() {
    label=$1 tries=$2
    shift 2
    i=0 streak=0
    while [ "$i" -lt "$tries" ]; do
        if "$@" >/dev/null 2>&1; then
            streak=$((streak + 1))
            [ "$streak" -ge "${STREAK:-3}" ] && return 0
        else
            streak=0
        fi
        i=$((i + 1))
        sleep 1
    done
    printf '\033[31merror:\033[0m %s did not come up within %ss\n' "$label" "$tries" >&2
    return 1
}

say "starting PostgreSQL"
docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
if ! docker run -d --name "$PG_NAME" -e POSTGRES_PASSWORD=e2e -e POSTGRES_DB=cypher \
    -p "$PG_PORT:5432" postgres:16-alpine >/dev/null; then
    fail "could not start PostgreSQL — is port $PG_PORT already taken? (E2E_PG_PORT overrides it)"
fi
# A real query, not pg_isready. The postgres image runs initdb against a
# TEMPORARY server first, and pg_isready answers yes to that one — so a probe
# that trusts it breaks out during initialisation and the very next command
# fails against a server that is restarting. That is exactly how this job died
# in CI, 1.4 seconds into a wait that was supposed to last forty. `select 1`
# against the named database is only true once the real server is serving it.
pg_ready() { docker exec "$PG_NAME" psql -U postgres -d cypher -c 'select 1'; }
if ! wait_for PostgreSQL 60 pg_ready; then
    # A container that started and then died leaves nothing in pg_isready's
    # output, so say what the container itself said.
    docker ps -a --filter "name=$PG_NAME" --format 'container: {{.Status}}' >&2 || true
    docker logs --tail 30 "$PG_NAME" >&2 2>&1 || true
    fail "PostgreSQL never became ready"
fi

# The UI has to be BUILT and embedded, or the panel serves a stale bundle and
# the suite tests whatever was there last time — which is the one failure mode
# that would make these tests actively misleading.
# E2E_SKIP_BUILD=1 expects cypherd and cypher-agent already at $WORK, with the
# UI already embedded — for a machine whose Go and pnpm live in a container.
if [ "${E2E_SKIP_BUILD:-}" = 1 ]; then
    [ -x "$WORK/cypherd" ] && [ -x "$WORK/cypher-agent" ] \
        || fail "E2E_SKIP_BUILD=1 but $WORK/cypherd and $WORK/cypher-agent are not both there"
    say "reusing the binaries in $WORK"
else
    say "building the web UI and embedding it"
    (cd "$ROOT/web" && pnpm install --frozen-lockfile >/dev/null && pnpm build >/dev/null)
    rm -rf "$ROOT/core/api/rest/webui/dist"
    cp -r "$ROOT/web/dist" "$ROOT/core/api/rest/webui/dist"
    say "building cypherd and cypher-agent"
    (cd "$ROOT/core" && go build -o "$WORK/cypherd" ./cmd/cypherd)
    (cd "$ROOT/agent" && CGO_ENABLED=0 go build -o "$WORK/cypher-agent" ./cmd/cypher-agent)
fi

say "booting the panel on $API"
export CYPHERD_DATABASE_URL="postgres://postgres:e2e@127.0.0.1:$PG_PORT/cypher?sslmode=disable"
CYPHERD_MASTER_KEY="$(head -c32 /dev/urandom | base64)"
export CYPHERD_MASTER_KEY
export CYPHERD_DATA_DIR="$WORK/data"
export CYPHERD_ADMIN_EMAIL="$EMAIL"
export CYPHERD_ADMIN_PASSWORD="$PASSWORD"
export CYPHERD_HTTP_ADDR=":$HTTP_PORT"
export CYPHERD_ENROLL_ADDR=":$ENROLL_PORT"
export CYPHERD_NATS_ADDR=":$NATS_PORT"
mkdir -p "$CYPHERD_DATA_DIR"
"$WORK/cypherd" > "$WORK/cypherd.log" 2>&1 &
echo $! > "$WORK/cypherd.pid"
if ! STREAK=1 wait_for "the panel" 60 curl -sf "$API/readyz"; then
    tail -30 "$WORK/cypherd.log" >&2
    fail "the panel never became ready"
fi

# An enrolled agent, because the create-application dialog offers only servers
# that are actually enrolled — so without one the screens under test cannot be
# reached at all.
if [ "${E2E_NO_AGENT:-}" = 1 ]; then
    say "E2E_NO_AGENT=1 — no agent; specs needing an enrolled server will fail"
else
say "enrolling an agent"
TOKEN=$(curl -sf -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | sed 's/.*"token":"\([^"]*\)".*/\1/')
[ -n "$TOKEN" ] || fail "could not sign in to the panel we just booted"
JOIN=$(curl -sf -X POST "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d '{"name":"e2e-host"}' \
    | sed 's/.*"join":{"token":"\([^"]*\)".*/\1/')
curl -sf "$API/api/v1/ca.pem" -o "$WORK/ca.pem"
"$WORK/cypher-agent" enroll --plane "127.0.0.1:$ENROLL_PORT" --token "$JOIN" \
    --ca-file "$WORK/ca.pem" --state-dir "$WORK/agent" --hostname e2e-host >/dev/null
"$WORK/cypher-agent" run --state-dir "$WORK/agent" --heartbeat 2s > "$WORK/agent.log" 2>&1 &
echo $! > "$WORK/agent.pid"
agent_running() {
    curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" | grep -q '"status":"running"'
}
if ! STREAK=1 wait_for "the agent" 30 agent_running; then
    tail -20 "$WORK/agent.log" >&2
    fail "the agent never reported running — the screens under test need an enrolled server"
fi
fi

say "running the browser suite"
docker run --rm --network host \
    -v "$ROOT/web/e2e:/e2e" -w /e2e \
    -e CI="${CI:-}" -e E2E_BASE_URL="$API" \
    -e E2E_EMAIL="$EMAIL" -e E2E_PASSWORD="$PASSWORD" \
    "$PLAYWRIGHT_IMAGE" \
    sh -c "npm i -D @playwright/test@1.49.0 --silent >/dev/null 2>&1 && npx playwright test ${*:-}"
