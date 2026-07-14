---
name: agent-config-generators
description: How CypherAgent generates service config files (nginx vhosts, PHP-FPM pools, mail, DNS) with Go text/template, path resolution, and validate-then-reload. Use when adding or changing any generated server config.
---

# Agent Config Generators

> **Status: code-grounded (Phase 3).** Realized by `internal/webserver` (nginx vhost + PHP-FPM pool renderers) and `internal/platform` `Sites` (apply layer), wired via the `site.provision`/`site.deprovision` agent tasks. Follow those as the reference implementation. Also read [[linux-system-integration]] and [[jobs-and-agent-tasks]].

## Core rules

- Configs are rendered with Go's `text/template` from typed structs — no string concatenation to build config bodies. **Rendering is pure and separate from applying**: renderers (`internal/webserver`) take a spec and return `[]byte` with no I/O, so they unit-test on any OS; the `platform.Sites` layer does the OS-touching apply. Keep that split.
- **All output paths resolve through the distro path-mapping layer (`internal/paths.Layout`)** — never hardcode `/etc/nginx/...`. Debian and RHEL differ; the Layout already encodes this and honors `CYPHER_PATH_*` overrides. Add a `Layout` field + helper if a needed path isn't there (as `VhostConfPath`, `PHPFPMPoolPath`, `PHPFPMSocketPath`, `AccountLogDir` do).
- **Validate, then reload — never blind restart.** Write the config, run the service's own validator (`nginx -t`, `postfix check`, `named-checkconf`, `pdnsutil check-zone`), and on failure **roll back the file** and return an error — never leave a broken config that takes down every site on the box (see `validateAndReloadNginx` + rollback in `sites_linux.go`). Reload, not restart. If the service binary isn't installed yet, degrade gracefully (write configs, skip reload) rather than failing — a box can be staged before the service lands.
- Generators run inside agent task handlers, so they inherit the idempotency requirement: rendering + applying the same desired state twice must converge, not error or duplicate. Deterministic output (e.g. sort map keys before templating) keeps it golden-stable.
- Files written into an account's tree (web root, logs) are created **owned by the account's system user** (mkdir+chown via the account uid/gid); system configs (vhost, pool) are root-owned.

## Testing (golden files)

Template output is verified with golden-file tests: render against a fixed input struct, compare to a committed `testdata/*.golden` file, update goldens deliberately. These run on any OS (pure rendering) — only the apply/reload step needs a Linux container (see [[testing-conventions]]).

## Adapter interface (modularity)

Each service category has ONE MVP default (nginx, PowerDNS, Postfix/Dovecot, Pure-FTPd) behind an interface; post-MVP alternatives (Apache, OpenLiteSpeed, BIND, ProFTPD) implement the same interface without touching feature code. Design the generator as `Render(desiredState) ([]byte, error)` + `Apply(ctx)` so a second implementation is a drop-in. When a post-MVP adapter lands, it updates this skill in the same PR.

## Safety

Files written into an account's tree are owned by that account's system user, not root. Reloads are debounced/coalesced where a batch of changes would otherwise trigger many reloads.
