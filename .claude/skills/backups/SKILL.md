---
name: backups
description: restic/Borg backup conventions — incremental/deduplicated snapshots, endpoint config, db-dump coordination, and restore testing. Use when working on backup scheduling, execution, or restore.
---

# Backups

> **Status: design-intent (pre-implementation).** Grounded in plan.md Sections 4A/4C (Backup Policies, CypherAgent Backups). Lands in Phase 6. Verify against code then, updating in the same PR. Read [[jobs-and-agent-tasks]] and [[filesystem-operations-safety]].

## Engine: incremental & deduplicated, always

- Scheduled backups use **restic or Borg** (incremental, chunk-level deduplicated) — **never repeated full tar/zip archives**. Full re-archiving of every account on every run is the fastest way to blow the disk/CPU budget at scale (plan.md).
- Raw `tar`/`zip` is reserved **only** for one-off manual export/download requests, never the scheduled path.

## Execution

- Backups run as **idempotent agent tasks** (see [[jobs-and-agent-tasks]]) off the request path — never block the UI. A rerun resumes/reconciles the snapshot repo, it doesn't duplicate.
- Coordinate **database dumps** with file backups so a snapshot is consistent (dump DBs, then snapshot files+dump together) — a file backup with a mid-write DB is not restorable.
- File reads honor account isolation (run as the account user where reading account data — see [[filesystem-operations-safety]]).

## Endpoints

- Destinations are config-driven: S3, SFTP, local paths natively (restic/Borg backends); `rclone` as a transport for destinations they don't speak natively (Google Drive, Dropbox — post-MVP). Endpoint credentials are secrets: encrypted at rest, never logged or placed in task payloads.

## Restore is the point — test it

- A backup that hasn't been restore-tested is not a backup. **Restore-path testing is a requirement**, not optional: verify snapshots restore to a usable state (at least periodically / in CI against a scratch target). Surface restore as a first-class operation, not an afterthought.
