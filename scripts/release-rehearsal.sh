#!/usr/bin/env bash
# A release, rehearsed end to end on one machine, before one is tagged.
#
# WHY THIS EXISTS. The release pipeline (.github/workflows/release.yml) is
# fifteen steps that have never all run in order, and three of them cannot be
# checked by any test in this repository:
#
#   - the build must be REPRODUCIBLE, byte for byte, or `make release-sign` can
#     never verify a rebuild and the offline key is unusable forever;
#   - an existing panel must MIGRATE in place, not just install fresh — the
#     upgrade path is the one every operator after the first takes;
#   - a snapshot must actually RESTORE, into a database that is not the one it
#     came from. "backups without tested restore fail P2" is in the feature
#     matrix, and an untested restore is not a backup.
#
# It touches nothing outside its own namespace. Everything it creates is called
# `cypher-rehearsal-*` and is removed on exit; it never starts an agent
# RECONCILER, so it cannot converge the host-global `cypher-proxy` container
# that a live agent on this machine owns.
#
#   ./scripts/release-rehearsal.sh            # builds everything
#   REHEARSAL_SKIP_BUILD=1 ./scripts/...      # reuse .rehearsal/dist
#
# REHEARSAL_GO is the command that runs a Go build, given one shell string. It
# defaults to running it here; a machine whose toolchain lives in a container
# points it at the wrapper, e.g. REHEARSAL_GO=/root/.cypher-tools/go.sh. The
# rest of the script needs only curl, git, docker and coreutils.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$ROOT/.rehearsal"
DIST="$WORK/dist"
PG_NAME=cypher-rehearsal-pg
MINIO_NAME=cypher-rehearsal-minio
PG_PORT="${REHEARSAL_PG_PORT:-15442}"
HTTP_PORT="${REHEARSAL_HTTP_PORT:-8098}"
ENROLL_PORT="${REHEARSAL_ENROLL_PORT:-8448}"
NATS_PORT="${REHEARSAL_NATS_PORT:-4228}"
MINIO_PORT="${REHEARSAL_MINIO_PORT:-9008}"
API="http://127.0.0.1:$HTTP_PORT"
VERSION="${REHEARSAL_VERSION:-v0.0.0-rehearsal}"
EMAIL=rehearsal@example.com
PASSWORD=rehearsal-password-1

RUN_GO="${REHEARSAL_GO:-sh -c}"
# The exact toolchain the release is built and signed with (go.work, read by
# release.yml and release-sign.sh). Any installed Go fetches it on demand.
GO_PIN="GOTOOLCHAIN=go$(awk '/^go /{print $2; exit}' "$ROOT/go.work")"
PASS=0; FAILED=0
say()  { printf '\n\033[36m== %s\033[0m\n' "$1"; }
ok()   { PASS=$((PASS+1)); printf '\033[32m  ok\033[0m %s\n' "$1"; }
bad()  { FAILED=$((FAILED+1)); printf '\033[31m  NO\033[0m %s\n' "$1"; }
fail() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

cleanup() {
    [ -f "$WORK/cypherd.pid" ] && kill "$(cat "$WORK/cypherd.pid")" 2>/dev/null || true
    docker rm -f "$PG_NAME" "$MINIO_NAME" >/dev/null 2>&1 || true
    rm -rf "$WORK/data" "$WORK/agent" "$WORK/cypherd.pid"
}
trap cleanup EXIT

# The rehearsal must not be able to disturb a panel that is already here. It
# starts no reconciler, so the proxy is safe by construction; the ports are the
# other way it could collide, and a port already answering is somebody's.
for p in "$PG_PORT" "$HTTP_PORT" "$ENROLL_PORT" "$NATS_PORT" "$MINIO_PORT"; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
        exec 3>&- 2>/dev/null || true
        fail "port $p is already in use — something else is listening. Set REHEARSAL_*_PORT."
    fi
done
mkdir -p "$WORK" "$DIST"

wait_for() {
    label=$1 tries=$2; shift 2
    i=0 streak=0
    while [ "$i" -lt "$tries" ]; do
        if "$@" >/dev/null 2>&1; then
            streak=$((streak + 1))
            [ "$streak" -ge "${STREAK:-3}" ] && return 0
        else streak=0; fi
        i=$((i + 1)); sleep 1
    done
    printf '\033[31merror:\033[0m %s did not come up within %ss\n' "$label" "$tries" >&2
    return 1
}

# ── 1. build the artifacts the way the release workflow does ─────────────────
#
# The ldflags are release.yml's, verbatim, including the COMMIT-derived build
# date. A wall-clock date would make the build unreproducible, which is the
# thing step 2 is about to check.
say "1. building the release artifacts"
if [ "${REHEARSAL_SKIP_BUILD:-}" = 1 ]; then
    [ -x "$DIST/cypherd-linux-amd64" ] || fail "REHEARSAL_SKIP_BUILD=1 but $DIST is empty"
    ok "reusing $DIST"
else
    COMMIT=$(git -C "$ROOT" rev-parse --short HEAD)
    BUILD_DATE=$(TZ=UTC git -C "$ROOT" log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)
    PUBKEYS=$(tr -d '[:space:]' < "$ROOT/release-pubkey.txt" 2>/dev/null || true)
    PLANE_STAMPS="-X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE -X github.com/MaramHarsha/cypherpanel/core/upgrade.ReleasePublicKey=$PUBKEYS"
    AGENT_STAMPS="-X main.version=$VERSION -X github.com/MaramHarsha/cypherpanel/agent/updater.publicKeys=$PUBKEYS"
    test -f "$ROOT/core/api/rest/webui/dist/index.html" \
        || fail "core/api/rest/webui/dist is missing — run 'make build-web'"
    ok "the embedded web UI is present"
    for arch in amd64 arm64; do
        $RUN_GO "cd '$ROOT/core' && $GO_PIN CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags '-s -w $PLANE_STAMPS' -o '$DIST/cypherd-linux-$arch' ./cmd/cypherd"
        $RUN_GO "cd '$ROOT/agent' && $GO_PIN CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags '-s -w $AGENT_STAMPS' -o '$DIST/cypher-agent-linux-$arch' ./cmd/cypher-agent"
    done
    $RUN_GO "cd '$ROOT/core' && $GO_PIN go run ./cmd/release-manifest -version '$VERSION' -published-at '$BUILD_DATE' -out '$DIST/release.json'" || fail "release.json"
    (cd "$DIST" && sha256sum cypher* release.json > SHA256SUMS)
    grep -q '"version": "'"$VERSION"'"' "$DIST/release.json" && ok "release.json names $VERSION" || bad "release.json is wrong"
    ok "built 4 binaries for 2 architectures with $($RUN_GO "cd '$ROOT' && $GO_PIN go env GOVERSION")"
fi

(cd "$DIST" && sha256sum -c SHA256SUMS >/dev/null) \
    && ok "SHA256SUMS verifies" || bad "SHA256SUMS does not verify"

# ── 2. reproducibility, which the offline signing key depends on ─────────────
say "2. rebuilding to check the build is reproducible"
if [ "${REHEARSAL_SKIP_BUILD:-}" = 1 ]; then
    printf '  -- skipped with the build\n'
else
    CHECK="$WORK/repro"; mkdir -p "$CHECK"
    $RUN_GO "cd '$ROOT/core' && $GO_PIN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w $PLANE_STAMPS' -o '$CHECK/cypherd-linux-amd64' ./cmd/cypherd"
    A=$(sha256sum "$DIST/cypherd-linux-amd64" | cut -d' ' -f1)
    B=$(sha256sum "$CHECK/cypherd-linux-amd64" | cut -d' ' -f1)
    if [ "$A" = "$B" ]; then
        ok "two independent builds are byte-identical ($A)"
    else
        bad "the build is NOT reproducible — 'make release-sign' can never verify a rebuild
      first:  $A
      second: $B"
    fi
    rm -rf "$CHECK"
fi

CYPHERD="$DIST/cypherd-linux-amd64"
AGENT="$DIST/cypher-agent-linux-amd64"
chmod +x "$CYPHERD" "$AGENT"

# ── 3. the smoke test the release job runs before publishing ─────────────────
say "3. smoke-testing the binary"
OUT="$("$CYPHERD" 2>&1 || true)"
case "$OUT" in
    *DATABASE_URL*|*database*|*config*) ok "starts and reaches config validation" ;;
    *) bad "unexpected startup output: $OUT" ;;
esac
"$CYPHERD" version | grep -q "$VERSION" \
    && ok "reports its version: $("$CYPHERD" version)" \
    || bad "the version stamp did not land"

# ── 4. a fresh install, on an empty database ─────────────────────────────────
say "4. installing onto an empty database"
docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
docker run -d --name "$PG_NAME" -e POSTGRES_PASSWORD=r -e POSTGRES_DB=cypher \
    -p "$PG_PORT:5432" postgres:16-alpine >/dev/null
pg() { docker exec "$PG_NAME" psql -U postgres -d "${1:-cypher}" -tAc "${2:-select 1}"; }
wait_for PostgreSQL 60 pg cypher 'select 1' || fail "PostgreSQL never became ready"
ok "PostgreSQL is up on $PG_PORT"

export CYPHERD_DATABASE_URL="postgres://postgres:r@127.0.0.1:$PG_PORT/cypher?sslmode=disable"
MASTER_KEY="$(head -c32 /dev/urandom | base64)"
export CYPHERD_MASTER_KEY="$MASTER_KEY"
export CYPHERD_DATA_DIR="$WORK/data"
export CYPHERD_HTTP_ADDR=":$HTTP_PORT"
export CYPHERD_ENROLL_ADDR=":$ENROLL_PORT"
export CYPHERD_NATS_ADDR=":$NATS_PORT"
mkdir -p "$CYPHERD_DATA_DIR"

boot() {
    "$1" > "$WORK/cypherd.log" 2>&1 &
    echo $! > "$WORK/cypherd.pid"
    STREAK=1 wait_for "the panel" 60 curl -sf "$API/readyz" || {
        tail -30 "$WORK/cypherd.log" >&2; return 1
    }
}
halt() {
    [ -f "$WORK/cypherd.pid" ] || return 0
    kill "$(cat "$WORK/cypherd.pid")" 2>/dev/null || true
    for _ in $(seq 20); do curl -sf "$API/readyz" >/dev/null 2>&1 || return 0; sleep 1; done
    return 1
}

boot "$CYPHERD" && ok "the panel booted and answered /readyz" || bad "the panel never came up"

# First-run setup is a real screen, and it happens exactly once. A release that
# cannot make its first account is a release nobody can use.
curl -sf -X POST "$API/api/v1/setup" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" >/dev/null 2>&1 \
    || curl -sf -X POST "$API/api/v1/auth/setup" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" >/dev/null 2>&1 \
    || true
login() {
    curl -sf -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | sed 's/.*"token":"\([^"]*\)".*/\1/'
}
TOKEN=$(login)
[ -n "$TOKEN" ] && ok "the owner account was created and signed in" || bad "could not create the owner account"

# The panel a browser gets, from the binary rather than from a dev server.
curl -sf "$API/" | grep -qi '<div id="root"\|<title' \
    && ok "the embedded panel is served" || bad "the embedded panel is missing"

SCHEMA_FRESH=$(pg cypher "select max(version_id) from goose_db_version")
ok "fresh install is at schema $SCHEMA_FRESH"

# ── 5. a server joins ────────────────────────────────────────────────────────
#
# `enroll` only. Running the agent's reconciler would converge the host-global
# `cypher-proxy` container, which on this machine belongs to a live panel.
say "5. enrolling a server"
JOIN=$(curl -sf -X POST "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d '{"name":"rehearsal-host"}' \
    | sed 's/.*"join":{"token":"\([^"]*\)".*/\1/')
curl -sf "$API/api/v1/ca.pem" -o "$WORK/ca.pem"
if "$AGENT" enroll --plane "127.0.0.1:$ENROLL_PORT" --token "$JOIN" \
    --ca-file "$WORK/ca.pem" --state-dir "$WORK/agent" --hostname rehearsal-host >/dev/null 2>&1; then
    ok "the agent enrolled over mTLS and holds a certificate"
else
    bad "enrolment failed"
fi
curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $TOKEN" | grep -q '"enrolled":true' \
    && ok "the panel shows the server joined" || bad "the server did not come back enrolled"

# Something to lose, so the steps below have something to prove they kept.
PROJECT=$(curl -sf -X POST "$API/api/v1/projects" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d '{"name":"rehearsal"}')
echo "$PROJECT" | grep -q '"id"' && ok "a project was created" || bad "could not create a project"

# ── 6. restart, the way a reboot does ────────────────────────────────────────
say "6. restarting the plane"
halt && ok "the panel stopped cleanly" || bad "the panel did not stop"
boot "$CYPHERD" && ok "it came back" || bad "it did not come back"
curl -sf "$API/api/v1/projects" -H "Authorization: Bearer $(login)" | grep -q rehearsal \
    && ok "the project survived the restart" || bad "the project did not survive"
curl -sf "$API/api/v1/servers" -H "Authorization: Bearer $(login)" | grep -q '"enrolled":true' \
    && ok "the server's identity survived" || bad "the server's identity did not survive"

# ── 7. the upgrade path, which is the one every operator after the first takes ─
say "7. upgrading in place from the previous release"
halt || bad "could not stop the panel for the upgrade"
if git -C "$ROOT" rev-parse --verify -q origin/main >/dev/null; then
    PREV="$WORK/cypherd-prev"
    if [ ! -x "$PREV" ]; then
        # Inside $WORK so a containerised toolchain, which sees only the
        # repository, can reach the checkout it is asked to build.
        TMP="$WORK/prev-src"; rm -rf "$TMP"; mkdir -p "$TMP"
        git -C "$ROOT" archive origin/main | tar -x -C "$TMP"
        $RUN_GO "cd '$TMP/core' && CGO_ENABLED=0 go build -o '$PREV' ./cmd/cypherd" >/dev/null 2>&1 \
            || PREV=""
        rm -rf "$TMP"
    fi
else
    PREV=""
fi
if [ -n "$PREV" ] && [ -x "$PREV" ]; then
    # A database at the PREVIOUS release's schema, made by that release's own
    # binary — not by this one with a migration held back, which would prove
    # nothing about the shape the old code actually left behind.
    docker exec "$PG_NAME" psql -U postgres -c 'drop database if exists upgrade' >/dev/null
    docker exec "$PG_NAME" psql -U postgres -c 'create database upgrade' >/dev/null
    OLD_URL="postgres://postgres:r@127.0.0.1:$PG_PORT/upgrade?sslmode=disable"
    CYPHERD_DATABASE_URL="$OLD_URL" "$PREV" migrate >/dev/null 2>&1 \
        || CYPHERD_DATABASE_URL="$OLD_URL" timeout 20 "$PREV" >/dev/null 2>&1 || true
    OLD_SCHEMA=$(pg upgrade "select max(version_id) from goose_db_version" 2>/dev/null || echo none)
    ok "a panel at the previous release is at schema $OLD_SCHEMA"
    if CYPHERD_DATABASE_URL="$OLD_URL" "$CYPHERD" migrate >/dev/null 2>&1; then
        NEW_SCHEMA=$(pg upgrade "select max(version_id) from goose_db_version")
        ok "the new binary migrated it to $NEW_SCHEMA"
        [ "$NEW_SCHEMA" = "$SCHEMA_FRESH" ] \
            && ok "an upgraded database matches a fresh install's schema" \
            || bad "upgraded schema $NEW_SCHEMA != fresh $SCHEMA_FRESH"
    else
        bad "the new binary could not migrate the previous release's database"
    fi
else
    printf '  -- skipped: could not build the previous release\n'
fi
boot "$CYPHERD" && ok "the panel is back up" || bad "the panel did not come back"
TOKEN=$(login)

# ── 8. a snapshot, and a restore into a database it did not come from ────────
say "8. backing the plane up and restoring it elsewhere"
docker rm -f "$MINIO_NAME" >/dev/null 2>&1 || true
docker run -d --name "$MINIO_NAME" -p "$MINIO_PORT:9000" \
    -e MINIO_ROOT_USER=rehearsal -e MINIO_ROOT_PASSWORD=rehearsalsecret \
    minio/minio:latest server /data >/dev/null 2>&1 || true
if wait_for MinIO 60 curl -sf "http://127.0.0.1:$MINIO_PORT/minio/health/live"; then
    ok "an S3 endpoint is up on $MINIO_PORT"
    docker run --rm --network host --entrypoint sh minio/mc:latest -c \
        "mc alias set r http://127.0.0.1:$MINIO_PORT rehearsal rehearsalsecret >/dev/null && mc mb --ignore-existing r/plane >/dev/null" \
        >/dev/null 2>&1 && ok "a bucket exists" || bad "could not create the bucket"

    TARGET=$(curl -sf -X POST "$API/api/v1/backup-targets" -H "Authorization: Bearer $TOKEN" \
        -H 'Content-Type: application/json' \
        -d "{\"name\":\"rehearsal\",\"endpoint\":\"http://127.0.0.1:$MINIO_PORT\",\"bucket\":\"plane\",\"region\":\"us-east-1\",\"access_key\":\"rehearsal\",\"secret_key\":\"rehearsalsecret\"}" \
        | sed -n 's/.*"id":"\([^"]*\)".*/\1/p') || TARGET=""
    if [ -n "$TARGET" ]; then
        ok "a backup target was configured"
        ARM=$(curl -sf -X PUT "$API/api/v1/panel/disaster-recovery" -H "Authorization: Bearer $TOKEN" \
            -H 'Content-Type: application/json' \
            -d "{\"target_id\":\"$TARGET\",\"generate\":true,\"path_prefix\":\"plane-state\"}") || ARM=""
        IDENTITY=$(printf '%s' "$ARM" | sed -n 's/.*\(AGE-SECRET-KEY-[A-Z0-9]*\).*/\1/p')
        if [ -n "$IDENTITY" ]; then
            printf '%s\n' "$IDENTITY" > "$WORK/recovery.key"; chmod 600 "$WORK/recovery.key"
            ok "disaster recovery is armed, and it generated a recovery key"
        else
            bad "arming returned no recovery key: $ARM"
        fi

        # Deliberately not -f: a failure here is interesting, and the body says
        # what the plane could not do.
        RUN=$(curl -s -X POST "$API/api/v1/panel/disaster-recovery/run" \
            -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}') || RUN=""
        KEY=$(printf '%s' "$RUN" | sed -n 's/.*"object_key":"\([^"]*\)".*/\1/p')
        if [ -n "$KEY" ]; then
            ok "a snapshot was written to s3://plane/$KEY"
        else
            bad "the snapshot run produced no object: $RUN"
        fi

        # THE RESTORE, into a database this snapshot has never seen. An
        # untested restore is not a backup, and every step above is only worth
        # something if this one works.
        if [ -n "$KEY" ] && [ -s "$WORK/recovery.key" ]; then
            docker exec "$PG_NAME" psql -U postgres -c 'drop database if exists restored' >/dev/null
            docker exec "$PG_NAME" psql -U postgres -c 'create database restored' >/dev/null
            REST_URL="postgres://postgres:r@127.0.0.1:$PG_PORT/restored?sslmode=disable"
            if AWS_ACCESS_KEY_ID=rehearsal AWS_SECRET_ACCESS_KEY=rehearsalsecret \
                "$CYPHERD" restore --from "s3://plane/$KEY" --endpoint "http://127.0.0.1:$MINIO_PORT" \
                    --identity "$WORK/recovery.key" --database-url "$REST_URL" \
                    > "$WORK/restore.log" 2>&1; then
                ok "the snapshot decrypted and restored into an empty database"
                [ "$(pg restored "select count(*) from projects where name = 'rehearsal'")" = 1 ] \
                    && ok "the project came back" || bad "the project did not come back"
                [ "$(pg restored "select count(*) from servers where name = 'rehearsal-host'")" = 1 ] \
                    && ok "the enrolled server came back" || bad "the server did not come back"
                grep -qi 'master key\|CYPHERD_MASTER_KEY' "$WORK/restore.log" \
                    && ok "it printed the master key line the operator has to keep" \
                    || bad "the restore did not print the master key"
            else
                bad "the restore failed: $(tail -3 "$WORK/restore.log" | tr '\n' ' ')"
            fi
        fi
    else
        bad "could not configure a backup target"
    fi
else
    printf '  -- skipped: MinIO did not start\n'
fi

# ── 9. the updater fails CLOSED with no key ──────────────────────────────────
say "9. the agent updater with no trusted key"
if [ -z "$(tr -d '[:space:]' < "$ROOT/release-pubkey.txt" 2>/dev/null || true)" ]; then
    ok "release-pubkey.txt is empty, so this build trusts no update key"
    printf '     the agent reports its updater "disabled" and never fetches a manifest.\n'
    printf '     That is the fail-closed state ADR-010 requires, not a bug — generating\n'
    printf '     the offline key is the release manager'"'"'s step (docs/dev/release-signing.md).\n'
else
    ok "release-pubkey.txt holds a key; updates will be signature-checked"
fi

say "result"
printf '  %s checks passed, %s failed\n\n' "$PASS" "$FAILED"
[ "$FAILED" -eq 0 ]
