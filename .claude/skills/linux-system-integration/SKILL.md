---
name: linux-system-integration
description: Conventions for CypherAgent code that touches the Linux OS — system users, distro path mapping, and (as they land) systemd units and cgroups. Use when writing anything that shells out to or configures a managed server.
---

# Linux System Integration

## Where OS-touching code lives

- Behind interfaces in `internal/platform`, real implementation in `_linux.go` build-tagged files, `!linux` stub returning `platform.ErrUnsupported`. Agent task handlers depend on the interface, never on `exec.Command` directly — that keeps handlers testable and gives the "unsupported on this platform" permanent-failure path for free.
- Host telemetry follows the same split (`internal/hoststats`: `/proc` + `statfs` on Linux, zeros elsewhere). Telemetry reads must never fail a heartbeat — degrade to zero, don't error.

## System users (the isolation primitive)

- Every hosted account gets a dedicated Linux user (plan.md §7). Creation via `useradd --create-home --shell /usr/sbin/nologin` — hosted users **never** get a system password; access is via panel/FTP/SSH keys.
- All operations are idempotent: `Create` on an existing user and `Remove` of a missing user return nil (check with `user.Lookup` first). This is required by the task pipeline's redelivery semantics.
- Wrap external command failures with the command context AND its combined output: `fmt.Errorf("platform: useradd %s: %w: %s", username, err, out)`.

## Paths: distro differences are data, not code

- Never hardcode `/etc/nginx/...`, `/home/...`, etc. Resolve through `internal/paths.Layout` — Debian and RHEL families differ (e.g. `sites-enabled` vs `conf.d`), detection reads `/etc/os-release` (ID + ID_LIKE), and every field is overridable via `CYPHER_PATH_*` env (config-file overrides later).
- New system locations = new `Layout` fields with per-family defaults + an env override + accessor helpers (`AccountHome`, `VhostConfPath` are the pattern). Unknown distros get generic defaults plus a production warning, not a crash.

## Rules for upcoming systemd/cgroups work (Phase 2+)

- Per-account limits are systemd slices/cgroups v2 (plan.md §7) — unit and slice files are generated artifacts (see the future agent-config-generators skill), placed via `Layout.SystemdUnitDir`, followed by `systemctl daemon-reload`.
- Validate-then-apply, never blind restarts; and any file written into an account's home must be owned by that account's user, not root.
- The agent runs as root but must hold **no per-account persistent processes** and no unbounded in-memory state — the <50MB idle RSS budget (measured: 3.6MiB) is a hard constraint on every design here.
