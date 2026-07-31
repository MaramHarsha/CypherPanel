# Backups

CypherPanel backs hosting accounts up with **restic**: incremental, chunk-level
deduplicated snapshots. Repeated full archives are never used — at cPanel-scale
account counts they exhaust disk and CPU budget long before anything else does.

## Requirements

Each managed server needs `restic` on `PATH` (or `CYPHER_RESTIC_BIN` pointing at
it). Without it the agent logs a warning at startup and `backup.*` tasks fail
with a clear "not installed" reason rather than a bare exec error.

Database dumps additionally need `mariadb-dump` or `mysqldump`, plus
`CYPHER_AGENT_MARIADB_DSN` configured on the agent.

## Destinations

A destination is a restic repository plus the credentials to unlock it. They are
fleet infrastructure, so only root admins manage them (**Backups** in the
sidebar); any reseller may back their own accounts up into one.

| Backend | `repository` example |
| --- | --- |
| `local` | `/var/backups/cypherpanel` |
| `s3` | `s3:s3.amazonaws.com/bucket/prefix` |
| `sftp` | `sftp:user@host:/srv/backups` |
| `rest` | `rest:https://backup.example.com/repo` |

Backend credentials (`AWS_ACCESS_KEY_ID`, …) go in the destination's `env` map
and reach restic through its process environment — never as argv, so they stay
out of the process table.

### The repository password is unrecoverable

It encrypts the repository. CypherPanel stores it AES-256-GCM encrypted and
**never** returns it through any API, for any role. If you lose it, the snapshots
in that repository cannot be decrypted by anyone, including you.

## How a backup runs

1. Core creates a `backup.run` task and an `account_backups` row.
2. The agent fetches the destination's credentials over the **mTLS gRPC
   channel** (`AgentService.FetchBackupCredentials`). They are deliberately not
   in the task payload: JetStream retains task messages, so a payload secret
   would outlive the task and be readable by anything with stream access. Core
   re-checks that the task belongs to the calling server *and* references the
   requested destination before releasing anything.
3. The agent dumps each active database (`--single-transaction`, so the account's
   site is not locked out for the dump's duration) into an agent-owned `0700`
   staging directory — outside every account home, so one account can neither
   read another's dump nor tamper with its own before the snapshot.
4. It snapshots the account home **and** the dump directory together. A file
   backup taken while a database is mid-write is not restorable; capturing both
   in one snapshot is what makes the pair consistent.
5. Retention (`forget --prune`) runs **only after** a successful snapshot, so a
   failed backup can never prune the good snapshots that preceded it.

Snapshots are tagged `account:<id>`, so one repository can hold many accounts
and each prunes independently.

### Symlinks

restic archives symlinks rather than following them, so an account cannot
smuggle files from outside its home into a snapshot by planting one. That is why
the backup can safely run as the agent's (root) user — which it must, to read
root-owned files inside an account home.

### Retention

The policy maps directly to `restic forget --keep-daily/--keep-weekly/--keep-monthly`.
An all-zero policy is refused at both the API and the engine: `restic forget`
with no `--keep-*` flags prunes **every** snapshot, and a silently emptied
repository is not a recoverable mistake.

## Scheduling

A destination's schedule is `off`, `daily`, or `weekly`. Core sweeps hourly and
dispatches one backup per active account for each due destination. Suspended
accounts are still backed up — they keep their files, and they are usually the
ones you most want a snapshot of. Failed and terminating accounts are skipped.

A per-account failure never stops the rest of the fleet, and the destination is
still marked run: not marking it would re-sweep on the next tick and pile
duplicate tasks on the accounts that already succeeded.

## Restore — test it

**A backup that has never been restored is not a backup.** Restore is a
first-class operation, not an afterthought.

Two targets:

- **Staging** (default) — lands in an agent-owned directory so you can inspect
  the contents before promoting anything. Safe.
- **In place** (`home`) — overwrites the account's live files. Irreversible, and
  gated behind explicit confirmation in both the UI and `cypherctl --yes`.

The payload never names a filesystem path; both targets are derived agent-side
from the distro path layout, so a crafted task cannot aim a restore at `/etc` or
another account's home.

Restore periodically as a matter of routine — into staging, against a real
snapshot. It is the only way to learn that a repository password, a backend
credential, or a dump step is broken *before* you need it.

## Deleting a destination

Removing a destination deletes the panel's record and its stored credentials and
stops scheduled backups writing to it. **The remote repository and its snapshots
are left untouched** — deleting a panel record must never destroy the only copy
of somebody's data. Delete those with your storage provider if you truly want
them gone.

## CLI

```sh
cypherctl backup destinations
cypherctl backup run     --account <id> --destination <id>
cypherctl backup list    --account <id>
cypherctl backup restore --account <id> --backup <id> --snapshot <id>
cypherctl backup restore --account <id> --backup <id> --snapshot <id> --in-place --yes
```
