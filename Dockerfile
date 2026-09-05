# CypherPanel control plane (cypherd) — a single static binary with the web UI
# embedded via go:embed (ADR-001). Three stages: build the web assets, build
# the Go binary that embeds them, ship a minimal runtime.
#
#   docker build -t cypherpanel/cypherd .
#
# The agent (cypher-agent) is NOT built here — it installs on each server via
# install/agent.sh (ADR-010).

# ── 1. web assets ────────────────────────────────────────────────────────────
FROM node:22-alpine AS web
RUN corepack enable
WORKDIR /src/web
# Install deps first for layer caching, then build.
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# The client is generated from the committed OpenAPI spec.
COPY core/api/rest/openapi.yaml /src/core/api/rest/openapi.yaml
RUN pnpm generate:api && pnpm build

# ── 2. cypherd binary (embeds web/dist) ──────────────────────────────────────
FROM golang:1.25-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
# Module graph first for caching (go.work + every module's go.mod/go.sum).
COPY go.work go.work.sum ./
COPY core/go.mod core/go.sum ./core/
COPY agent/go.mod agent/go.sum ./agent/
COPY pkg/go.mod pkg/go.sum ./pkg/
RUN cd core && go mod download
COPY . .
# Drop the built web assets into the go:embed path, then build.
COPY --from=web /src/web/dist ./core/api/rest/webui/dist
# Stamped into the binary and served by GET /api/v1/panel/version and
# `cypherd version` (docs/features/control-plane-hardening.md §3). Unset, they
# stay "dev", which is also what tells the update check there is no release to
# compare against.
ARG VERSION=dev
ARG COMMIT=dev
ARG BUILD_DATE=""
RUN cd core && CGO_ENABLED=0 go build \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/cypherd ./cmd/cypherd

# ── 3. runtime ───────────────────────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 cypherd
COPY --from=build /out/cypherd /usr/local/bin/cypherd
# Durable state (JetStream WORK stream, runtime logs) lives here; mount a volume.
RUN mkdir -p /var/lib/cypherd && chown cypherd /var/lib/cypherd
USER cypherd
ENV CYPHERD_DATA_DIR=/var/lib/cypherd
# HTTP (UI + API), gRPC enrollment, NATS (agent data plane).
EXPOSE 8080 8443 4222
VOLUME ["/var/lib/cypherd"]
ENTRYPOINT ["/usr/local/bin/cypherd"]
