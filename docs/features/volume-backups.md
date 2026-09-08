# Named volumes in the backup schedule

Design canvas `13g` ("App · Storage"), whose own caption says of the *in
backups* flag: *"Would need its own spec."* This is it.

## 1. What exists, and what does not

An Application already declares **named volumes** — `AppVolume{name, path}`,
with the Docker volume name derived from the label — and the agent already
creates and mounts them. They are the one part of a container that deliberately
survives a rollout: everything else is thrown away and rebuilt from the image,
which is what makes a deploy repeatable.

What does not exist is any way to get a copy of what is inside one. Managed
databases have the whole story — a schedule, an S3 target, a retention count,
history, and a restore — and application volumes have none of it. So the panel
today can tell an operator that their uploads directory survives a deploy, and
cannot tell them what happens when the server does not.

That asymmetry is the feature. Not a new subsystem: the schedule, the target,
the retention sweep, the history rows and the restore flow are all built and
running for databases, and this is the same machinery pointed at a different
source of bytes.

## 2. The one honest difficulty: a volume is not a dump

A database backup is **consistent by construction**. `pg_dump` and `mysqldump`
ask the engine for a transactionally coherent view, so the artefact is a
snapshot of a state the database was actually in.

A volume has no such thing to ask. `tar` over a live directory reads files one
at a time while the application keeps writing, so the archive can contain the
first half of a file and not the second, or file A from before a write and file
B from after. For a directory of finished uploads this is almost always fine.
For SQLite, LevelDB, a search index, or anything else maintaining its own
on-disk invariants, it is a corrupt copy that looks like a good one.

**The spec's answer is to say so, not to solve it.** Three approaches were
considered:

- **Stop the container, archive, start it.** Correct, and it turns a backup into
  an outage. Vision non-negotiable 4 is that downtime is opt-in and never a
  surprise; a nightly schedule that silently stops production at 03:00 is the
  purest form of that surprise.
- **Filesystem snapshots** (LVM, ZFS, overlay). Genuinely consistent, and
  entirely outside what ADR-006 lets us assume: the agent talks to a Docker
  daemon, not to a storage layer, and the set of hosts where this works is not
  knowable from here.
- **Say what the artefact is.** A *crash-consistent* copy — the same thing you
  would have if the power had been cut at that instant. Applications that
  survive power loss survive this; ones that do not, do not.

The third is what ships, and the words appear in three places: on the volume
row when the flag is turned on, in the archive's own manifest, and on the
restore confirm. An operator who reads "crash-consistent" once and decides it is
fine for their uploads has made an informed choice; one who was told "backed up"
and discovers otherwise during a recovery has been misled by us.

The one mitigation that costs nothing is offered: a volume marked
`quiesce: stop` is archived with its container stopped, and the row says the
outage is the price. It is off by default and it is per volume, not per
application.

## 3. The shape

An `AppVolume` gains one field:

```
AppVolume:
  name    string      -- unchanged
  path    string      -- unchanged
  backed_up bool      -- NEW, default false
  quiesce string      -- NEW, "" | "stop", default ""
```

Additive on an existing embedded JSON structure (`applications.volumes` is
JSONB), so there is **no migration for the flag itself** — an older row decodes
with `backed_up: false`, which is the behaviour it has today.

What does need schema is where the schedule lives. Two options were weighed:

- **Reuse `database_backups` with a nullable application id.** Rejected: the
  table's every column assumes an engine — the dump command, the restore path,
  the `database_id` foreign key — and making half of them nullable to admit a
  second kind of subject is how a clean table becomes a union type nobody can
  read.
- **A sibling table, `volume_backups`,** keyed on `application_id`, carrying the
  same four columns that make a schedule (`target_id`, `schedule`,
  `retention_count`, `enabled`). It shares the *records* table with databases —
  `backup_records` gains a nullable `application_id` beside its nullable
  `database_id`, exactly one of which is set — because history, retention
  pruning and the S3 object lifecycle are identical and duplicating them would
  duplicate the sweeper too.

The second ships.

## 4. How the bytes move

The agent already has the exact primitive. `BackupExecutor.archiveOutGzip`
streams a path out of a container through the Docker archive API and gzips it,
and `ExecuteBackup` uploads the result to S3 with SigV4. A database backup only
differs in what produces the bytes: it execs a dump command first, then archives
the file.

A volume backup skips the exec and archives the mount path directly. That is the
whole delta — one new work message, no new transport, no shell string on the
wire, and the same `S3Client` and the same retention prune.

**It runs on the server that mounts the volume**, which is the server the
application runs on. There is no relay: a volume is host-local state and moving
it between hosts is a migration, not a backup.

## 5. Restore is deliberately narrower than a database's

A database restore stops the engine, replaces its data, and starts it — because
a managed database is a thing the panel created and wholly owns.

A volume restore **writes files into a directory an application is using**, and
the panel does not know what that application will do about it. So:

- The container is **stopped for the restore, always** — no opt-out. Writing
  under a running process is the corruption case from §2 with the causality
  reversed, and the one place this spec refuses to offer a choice.
- The restore **replaces the volume's contents** rather than merging: a merge
  leaves files the archive does not contain, which is a state that was never
  backed up and never existed.
- It is a **typed-name confirm** with the outage in the blast radius, the same
  shape `ConfirmDestructive` gives every other irreversible act.

## 6. Deliberately out of scope

- **Bind mounts.** Only named volumes are eligible. A host path is the host's to
  back up, and archiving arbitrary host directories through the agent is a file
  read primitive we are not adding (threat-model: the agent has no
  read-any-path verb, and this would be one).
- **Compose stack volumes.** A stack's volumes are compose's, named by compose,
  and the panel does not model them. When it does, this machinery extends;
  until then the spec does not pretend to cover them.
- **Incremental or deduplicated backups.** Every run is a full archive. Restic
  and borg exist and are better at this than we will be; the S3 target is
  ordinary object storage and an operator who needs incrementals can point one
  of those at the same bucket.
- **Cross-server restore.** §4's reasoning: that is a migration.
- **Backing up a volume no application mounts.** An orphaned volume has no
  schedule to hang off and no container to quiesce; desired-state GC is what
  deals with it.
