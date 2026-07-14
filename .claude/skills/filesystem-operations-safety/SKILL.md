---
name: filesystem-operations-safety
description: Safety rules for any code touching user files — File Manager, FTP, backups. Path-traversal prevention, privilege dropping, quotas, and safe archive/upload handling. Use for any code that reads or writes hosted-account files.
---

# Filesystem Operations Safety

> **Status: design-intent (pre-implementation).** Grounded in plan.md Sections 4B/7 (File Manager, isolation model). Lands in Phase 4. This is the highest-risk surface in the product — a bug here is a cross-account breach. Verify against code as it lands, updating in the same PR. Read [[linux-system-integration]].

## Path-traversal prevention (mandatory on every path input)

- Every user-supplied path is treated as hostile. Resolve it against the account root, then **canonicalize (`filepath.Clean` + resolve symlinks) and verify the result is still under the account root** before any operation. Reject `..`, absolute paths, and paths that escape after symlink resolution.
- Never build a filesystem path by concatenating user input onto a base with string ops — join, clean, then re-check the prefix. A passing prefix check on the *un-canonicalized* path is not sufficient (symlinks).

## Operate as the account user, never root

- File operations run with the **hosted account's uid/gid**, not the agent's root — so the OS enforces isolation even if an application-level check is missed. Drop privileges (or perform ops via a helper running as the user); never `os.Open` a user's file as root and trust app-level checks alone.
- Created files/dirs get the account user's ownership and sane modes.

## Archives & uploads (zip-slip and friends)

- Extracting archives: validate **each entry's** destination path with the same canonicalize-and-verify-under-root check *before* writing (zip-slip: an entry named `../../etc/passwd`). Reject entries that escape.
- Enforce limits during extraction/upload: max total size, max entry count, and reject symlink/hardlink entries that point outside the root. Guard against decompression bombs (cap expanded size).
- Uploads stream to a temp file under the account root and are moved into place only after checks pass.

## Quotas & accounting

- Respect the account's package limits: disk **MB and inode** counts (many small files exhaust inodes before bytes). Check quota before large writes; surface quota-exceeded as a clear, non-fatal user error.

## General

- Symlinks are the recurring danger — re-verify after every resolution, never trust a path you checked before a later `mkdir`/rename could have changed it (TOCTOU: prefer operating on file descriptors / `openat`-style flows where the platform layer supports it).
