---
name: agent-config-generators
description: How CypherAgent generates service config files (nginx vhosts, PHP-FPM pools, mail, DNS) with Go text/template, path resolution, and validate-then-reload. Use when adding or changing any generated server config.
---

# Agent Config Generators

> **Status: design-intent (pre-implementation).** Grounded in plan.md (CypherAgent Config Generators, Section 4C) and the established Phase 1 patterns. First real generator lands in Phase 3 (nginx). Verify/expand against code then, updating in the same PR. Also read [[linux-system-integration]] and [[jobs-and-agent-tasks]].

## Core rules

- Configs are rendered with Go's `text/template` from typed structs — no string concatenation to build config bodies.
- **All output paths resolve through the distro path-mapping layer (`internal/paths.Layout`)** — never hardcode `/etc/nginx/...`. Debian and RHEL differ; the Layout already encodes this and honors `CYPHER_PATH_*` overrides. Add a `Layout` field if a needed path isn't there.
- **Validate, then reload — never blind restart.** Sequence: write to a temp file → run the service's own validator (`nginx -t`, `postfix check`, `named-checkconf`, `pdnsutil check-zone`) → atomically move into place → reload (not restart) the service. If validation fails, discard and fail the task; never leave a broken config that takes down every site on the box.
- Generators run inside agent task handlers, so they inherit the idempotency requirement: rendering + applying the same desired state twice must converge, not error or duplicate.

## Testing (golden files)

Template output is verified with golden-file tests: render against a fixed input struct, compare to a committed `testdata/*.golden` file, update goldens deliberately. These run on any OS (pure rendering) — only the apply/reload step needs a Linux container (see [[testing-conventions]]).

## Adapter interface (modularity)

Each service category has ONE MVP default (nginx, PowerDNS, Postfix/Dovecot, Pure-FTPd) behind an interface; post-MVP alternatives (Apache, OpenLiteSpeed, BIND, ProFTPD) implement the same interface without touching feature code. Design the generator as `Render(desiredState) ([]byte, error)` + `Apply(ctx)` so a second implementation is a drop-in. When a post-MVP adapter lands, it updates this skill in the same PR.

## Safety

Files written into an account's tree are owned by that account's system user, not root. Reloads are debounced/coalesced where a batch of changes would otherwise trigger many reloads.
