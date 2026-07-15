---
name: installer-and-packaging
description: Shell installer & packaging conventions from the cPanel-installer analysis (plan.md Appendix A). Use when writing the single-command installer, uninstaller, or release packaging.
---

# Installer & Packaging

> **Status: code-grounded (Phase 6).** `scripts/install.sh` + `scripts/uninstall.sh` implement the principles below — distro-detect via /etc/os-release, detect-and-ask (`--take-over`, never purge), no auto-reboot, GPG+sha256 verify, `--dry-run`/`--noexec`, opt-in add-ons, working uninstaller, 1GB target, POSIX sh + LF. Follow/extend those. **Still to add:** NATS server-side auth config generation, and the full 1GB-VM CI install test (a dry-run tier exists).

## What to copy from cPanel's installer

- **GPG-verify every downloaded artifact** (they fetch a signing key and verify) and checksum any self-extracting payload before running it.
- **Release tiers**: support `stable` / `edge` (and a pinned-version flag) so operators choose their update channel.
- Provide inspection flags (a `--noexec`-style "extract but don't run" and a dry-run) so admins can audit before executing.

## What to do DIFFERENTLY from cPanel (these are the differentiators)

- **Detect-and-ask, never silently purge.** cPanel force-removes conflicting packages with `rpm -e --nodeps`. CypherPanel detects conflicts and **asks**, or requires an explicit `--take-over` flag — never destroy an operator's existing services without consent.
- **Never auto-reboot.** cPanel may reboot; we don't. Surface "a reboot is recommended" and let the operator decide.
- **Ship a working uninstaller from v1.** cPanel has none; "you can actually remove it" is a genuine adoption argument. The installer that adds must have a counterpart that cleanly removes.
- **Opt-in add-ons, not opt-out.** cPanel auto-installs commercial bundles (Imunify, WP Toolkit) unless skipped. Every CypherPanel add-on installs only when explicitly enabled.
- **1GB-RAM install target.** cPanel's installer hard-fails below 2GB (it ships a private Perl runtime). Our Go static binaries need no such thing — protect the "installs where cPanel refuses" claim with a CI install test on a 1GB VM.

## Packaging hygiene

- Installer scripts, templates, and configs are **LF-only** (enforced by `.gitattributes` — a CRLF shell script silently breaks on Linux; this is the classic Windows-contributor trap).
- Binaries are the cross-compiled static Linux artifacts (`CGO_ENABLED=0`, amd64 + arm64) from `make build`; no runtime interpreter dependency.
- Distro-agnostic: detect the distro family (as the agent does via `/etc/os-release`) and drive per-family paths/package managers through data, not branches scattered in code.
