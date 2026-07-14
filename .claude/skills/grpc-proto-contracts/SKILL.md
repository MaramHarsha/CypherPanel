---
name: grpc-proto-contracts
description: Rules for evolving the CypherCore↔CypherAgent gRPC contract in proto/. Use when changing .proto files, regenerating gRPC code, or touching the mTLS channel.
---

# gRPC Proto Contracts

## The contract is the source of truth

- `proto/agent/v1/agent.proto` defines the Core↔Agent interface. Generated code lands in `gen/agent/v1` (committed to the repo so contributors don't need buf installed).
- Regenerate with `make proto` (buf + protoc-gen-go + protoc-gen-go-grpc; **no system protoc needed** — buf has a built-in compiler). Never hand-edit anything in `gen/`.

## Evolution rules (rolling-upgrade safety)

Core and Agent versions **will** differ across a large fleet during upgrades. Therefore:

- **Never reuse or renumber a field** once shipped. Deleted fields get `reserved N;` declarations.
- Only add new optional fields (proto3 fields are optional by default) — never change a field's type or meaning.
- New RPCs are fine; removing or renaming RPCs is a breaking change requiring a new package version (`cypherpanel.agent.v2`), not an edit to v1.
- CI runs `buf breaking` against the base branch; treat its failures as hard errors.
- Unknown-input tolerance goes both ways: servers validate required semantic fields explicitly (see `agentrpc.Server.Register` rejecting empty hostname) rather than assuming senders are current.

## Server/client conventions

- The Core-side service implementation lives in `internal/agentrpc`, embeds `agentv1.UnimplementedAgentServiceServer`, and returns `google.golang.org/grpc/status` errors with meaningful codes (`InvalidArgument`, `NotFound`, `Internal`). Agents branch on codes (e.g. `NotFound` on heartbeat → re-register).
- mTLS is the authorization boundary: possession of a CA-signed client cert is what allows talking to AgentService at all. TLS configs come from `internal/pki` (`ServerTLS` requires and verifies client certs, TLS 1.3 minimum). Plaintext gRPC is permitted only in development; `config.LoadCore`/`LoadAgent` enforce cert material in production.
- Certificates: `cypher-core pki init` (CA, refuses overwrite), `pki issue-server` (with DNS/IP SANs), `pki issue-agent`. ECDSA P-256. Keys are 0600, never committed (gitignored globs `*.key`/`*.crt`/`*.pem`).
- Naming note: proto snake_case fields with digits generate surprising Go names (`load_1m` → `Load_1M`) — check `gen/` before guessing a getter name.
