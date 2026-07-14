---
name: php-runtime-management
description: Multi-PHP install layout, per-account PHP-FPM pool isolation, INI overrides, and EOL-branch handling. Use when working on PHP version selection, FPM pools, or the MultiPHP INI editor.
---

# PHP Runtime Management

> **Status: design-intent (pre-implementation).** Grounded in plan.md Sections 4B/5B and the isolation model (Section 7). Lands in Phase 3. Verify against code then, updating in the same PR. Read [[agent-config-generators]] and [[linux-system-integration]] first.

## Version policy

- Offer **all currently-supported PHP branches** — resolve the live list from php.net's supported-versions at implementation time (do not hardcode a version set that will be EOL by launch; see plan.md Version Policy).
- Still offer the last 1-2 EOL branches for legacy-app compatibility, but **flag them as insecure in the UI**. Never present an EOL branch as if maintained.

## Per-account isolation (the security core)

- Each hosting account gets a **dedicated PHP-FPM pool** with its **own socket** (e.g. `/run/php/cyph_user12.sock`), running as that account's Linux user. The web server routes the account's PHP exclusively through that socket. This is what prevents cross-account code execution — never share a pool between accounts.
- Pool files are generated (see [[agent-config-generators]]): rendered from a typed struct, path via `internal/paths.Layout.PHPFPMPoolDir` (Debian vs RHEL/Remi trees differ), validated, then FPM reloaded — not restarted.
- Per-account resource caps (`pm.max_children`, memory) derive from the account's package limits, tying into the cgroups slice from the isolation model.

## INI overrides (MultiPHP INI editor)

- Expose a bounded, validated set of `php.ini` directives per account/domain (`memory_limit`, `upload_max_filesize`, `max_execution_time`, …) — an allowlist, not arbitrary INI injection.
- Overrides are applied as pool-level `php_admin_value`/`php_value` (so users can't override hard limits) or per-directory `.user.ini`, generated and validated like any other config.
