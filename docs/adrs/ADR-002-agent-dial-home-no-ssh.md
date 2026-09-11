# ADR-002: Dial-home agent over mTLS; no SSH orchestration

- **Status:** Accepted
- **Date:** 2026-07-17

## Context

Coolify orchestrates every server over SSH from the control plane. This requires storing private SSH keys in the panel's database (its single biggest security liability — a panel compromise yields root shells fleet-wide), demands connection multiplexing machinery to stay performant (`coolify/app/Helpers/SshMultiplexingHelper.php`, `SshRetryHandler.php`, `bootstrap/helpers/remoteProcess.php`), and still leaks: there are dedicated cleanup jobs for stale multiplexed connections. Tellingly, Coolify later added "Sentinel" — a push-based metrics agent (`CheckAndStartSentinelJob.php`, `PushServerUpdateJob.php`) — a tacit admission that SSH polling doesn't scale.

Dokploy avoids SSH by requiring Docker Swarm membership, which trades the problem for orchestrator lock-in and requires open ports between nodes.

## Decision

Every managed server runs **cypher-agent**, which makes a single **outbound** mTLS connection (gRPC + NATS subjects) to the control plane. Enrollment: a one-shot `curl | sh` installer carries a short-lived, single-use **join token**; the agent presents it and receives a client certificate. The control plane never initiates connections to servers and stores no server credentials of any kind.

## Alternatives considered

- **SSH orchestration (Coolify model).** Rejected: credential liability, chattiness, and the mux/retry tax described above.
- **Swarm/cluster-native control (Dokploy model).** Rejected: couples us to one orchestrator and to inter-node port requirements.
- **Pull-based polling agent (no persistent connection).** Simpler, but adds latency to every command and makes log/terminal streaming awkward. Rejected in favor of a persistent bidirectional stream with reconnect + replay.

## Consequences

- Servers behind NAT/firewalls work with zero inbound ports open.
- We must own an agent lifecycle: versioned releases, self-update channel, and backward-compatible wire protocol (see ENGINEERING.md proto rules).
- Offline servers catch up via JetStream durable consumers (ADR-003) instead of failed SSH retries.
- Interactive features (terminal, `docker exec`) are implemented as gRPC streams through the agent — feature parity with SSH without the credentials.
