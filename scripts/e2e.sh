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
    [ -f "$WORK/cypherd.pid" ] && kill "$(cat "$WORK/cypherd.pid")" 2>/dev/null || true
    [ -f "$WORK/agent.pid" ] && kill "$(cat "$WORK/agent.pid")" 2>/dev/null || true
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

mkdir -p "$WORK"

say "starting PostgreSQL"
docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
docker run -d --name "$PG_NAME" -e POSTGRES_PASSWORD=e2e -e POSTGRES_DB=cypher \
    -p "$PG_PORT:5432" postgres:16-alpine >/dev/null
for _ in $(seq 1 40); do
    docker exec "$PG_NAME" pg_isready -U postgres >/dev/null 2>&1 && break
    sleep 1
done
docker exec "$PG_NAME" pg_isready -U postgres >/dev/null 2>&1 || fail "PostgreSQL never became ready"

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
for _ in $(seq 1 60); do
    curl -sf "$API/readyz" >/dev/null 2>&1 && break
    sleep 1
done
curl -sf "$API/readyz" >/dev/null 2>&1 || { tail -30 "$WORK/cypherd.log"; fail "the panel never became ready"; }

# An enrolled agent, because the create-application dialog offers only servers
# that are actually enrolled — so without one the screens under test cannot be
# reached at all.
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
for _ in $(seq 1 30); do
    curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" | grep -q '"status":"running"' && break
    sleep 1
done

say "running the browser suite"
docker run --rm --network host \
    -v "$ROOT/web/e2e:/e2e" -w /e2e \
    -e CI="${CI:-}" -e E2E_BASE_URL="$API" \
    -e E2E_EMAIL="$EMAIL" -e E2E_PASSWORD="$PASSWORD" \
    "$PLAYWRIGHT_IMAGE" \
    sh -c "npm i -D @playwright/test@1.49.0 --silent >/dev/null 2>&1 && npx playwright test ${*:-}"
