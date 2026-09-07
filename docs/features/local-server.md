# Feature spec: Use this machine

> One button on the Join a server dialog that enrolls **the panel's own host**
> as a server, with no command to paste. The feature matrix has named it since
> the onboarding row was written — *"add server (join command or **use this
> machine**)"* — and it is the single most common first server: someone
> installs the panel on a VPS and wants to deploy something to that same VPS.
>
> Written 2026-09-07, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why the paste does not already cover this

It does, mechanically — the join command works on the panel's own host as well
as any other, and that is worth stating because it means this feature is
*convenience*, not a missing capability, and it must not acquire powers the
paste does not have.

What it is not is discoverable. The dialog hands you a line to run "on the
server you want to add", and the machine you are already signed into is not
obviously one of those. An operator on a single-VPS install reads that dialog,
opens a second terminal, SSHes into the box the browser is already talking to,
and pastes a command whose whole content the panel already knows. Every part of
that is avoidable.

## 2. The plane cannot do it, and that is deliberate

`cypherd.service` runs with `DynamicUser=true`, `ProtectSystem=strict` and
`NoNewPrivileges=true`. Installing an agent means writing
`/usr/local/bin/cypher-agent`, writing a unit into `/etc/systemd/system` and
calling `systemctl` — three things that sandbox exists to forbid. Relaxing it
would convert any RCE in the API surface into persistence on the control-plane
host, which is exactly the blast radius
[panel-updates.md](panel-updates.md) §3 already refused for the upgrade path.
It would be strange to refuse it there and grant it here for a smaller reason.

So the plane's entire power is to **write a request file** into a directory it
shares with a root one-shot, and this reuses that mechanism rather than
inventing a second one: same group (`cypherpanel-upgrade`), same directory,
same atomic write and same status file read back. A reader who understands the
upgrade handoff understands this one, and there is one trust boundary to audit
instead of two.

**ADR-002 is not bent.** ADR-002 says the plane never reaches out to a server —
no SSH, no push, no inbound port on the box. Nothing here reaches out to
anything: the request never leaves the host, carries no address, opens no
connection, and holds no remote credential. The agent still dials home to enroll
exactly as a pasted command's agent does. What changes is who types the command,
not who initiates the connection.

**It can only ever be this host.** The request has no field naming a machine,
and there is nothing to add one to — the helper runs on the box it is already
on. That is what keeps a later edit from quietly turning this into a
remote-execution primitive: there is no address to supply.

## 3. What the button does

1. The panel checks it is **able** (§5) and that this host is not already
   enrolled (§6). Either answer is shown before the button, not after it.
2. It creates the Server row and mints a join token through the ordinary
   `servers.Create` path — the same code the paste uses, so there is one way a
   Server comes into existence.
3. It writes `localjoin.json` into the handoff directory: the token, the enroll
   address, the panel's own URL, the CA fingerprint, an actor, a nonce and a
   two-minute expiry.
4. `cypherd-localjoin.path` notices the file and starts
   `cypherd-localjoin.service`, a root one-shot which runs the **same
   `install/agent.sh`** the paste runs, with the same variables.
5. The helper writes `localjoin-status.json` as it goes. The dialog polls it,
   then falls silent once the server heartbeats — because at that point the
   fleet table is the answer and a second progress display is noise.

Step 4 is the important one: it runs *the installer*, not a reimplementation of
it. Everything the paste path learned — the Docker check, the CA pin, the ELF
sanity check, the role flag, the systemd unit — applies unchanged, and a fix to
`agent.sh` fixes both paths at once.

## 4. The token is short-lived and the request is not desired state

The request expires in **two minutes**. It is a one-shot a person just clicked,
and the file is deleted by the helper before it does any work — a stale request
left by a crash must not enroll a second agent an hour later. It is deliberately
not a `desired_local_agent: true` the host converges on: that is an auto-install
with extra steps, and the same shape ADR-010 §3 refuses for the fleet.

The join token inside it is the ordinary single-use, short-lived credential the
paste carries. It is written 0640 into a directory only root and the helper's
group can read, which is a shorter and better-guarded life than the same token
has sitting in a terminal's scrollback.

## 5. Availability is reported, never assumed

Three things must be true, and each is checked and *named* rather than
discovered by a failure:

| State | What the panel says |
|---|---|
| `available` | the handoff directory exists and is writable |
| `unsupported` | this panel runs in a container (the compose install), where there is no host systemd to install into — the same `manual` mode panel updates reports, for the same reason |
| `helper_missing` | the directory is absent: this panel was installed before the helper existed, so `install.sh` needs re-running |
| `already_joined` | a server on this panel is already reporting this host |

`helper_missing` matters more than it looks: this feature adds two unit files to
`install.sh`, so every panel installed before today is in that state, and a
button that simply failed would look like a bug rather than a version gap.

## 6. Recognising "this host is already a server"

The agent writes its identity to `/var/lib/cypher-agent/identity.json`, and the
plane can read whether that path exists — same machine, no privilege needed. If
it does, its `server_id` names the Server, and the dialog offers a link to it
instead of a button.

That check is the honest one: asking the fleet table "is any server's hostname
mine?" would be a guess (two servers can share a hostname; a rename would break
it), while the identity file is the agent's own record of which Server it *is*.

## 7. API

| Route | Rank | Notes |
|---|---|---|
| `GET /api/v1/servers/local` | admin | the state from §5, plus the already-joined Server when there is one |
| `POST /api/v1/servers/local` | **owner, session-only** | creates the Server, mints the token, writes the request |

Owner and session-only, and the reason is the same one break glass and the agent
channel already carry: this installs software on the panel's own host as root. An
API token that can do that is an API token that owns the box, and API tokens live
in CI. `POST /api/v1/servers` stays panel-admin and token-reachable, because
handing out a join command grants nothing by itself.

`server.created` covers the Server row through the existing path; the local
install adds `server.local_join_requested` so the audit log distinguishes "an
admin generated a join command" from "an owner installed an agent on the control
plane", which are different acts with different blast radii.

## 8. UI

The Join a server dialog gains a second, quieter path above the command block:

> **This machine** · `vmi3338398` — install the agent here, no command needed.
> **[ Use this machine ]**

The paste stays exactly where it is and stays the primary: most servers are not
this one. When the state is not `available` the button is replaced by the
sentence that says why, because a disabled control with no explanation is the
dead end ui-principles §11 is named against.

While it runs, the dialog shows the helper's own phase — `installing the agent`,
`enrolling`, `waiting for the first heartbeat` — from `localjoin-status.json`.
It is a real phase from a real process, not a spinner on a timer (ui-principles
§3).

## 9. Deliberately out of scope

- **Removing the local agent from the panel.** Deleting the Server revokes its
  identity, which is the security-relevant half and already works. Uninstalling
  a systemd unit is a second root verb for a much rarer act, and
  `systemctl disable --now cypher-agent` is one line.
- **Installing an agent on any host but this one.** §2. There is no address
  field, and there must never be one.
- **A role picker.** The local agent installs as `all`, which is what a
  single-VPS panel wants. A builder-only control plane is a real configuration
  and it is a paste, because choosing it is a deliberate act.
- **Making this part of `install.sh`.** An operator who wants the panel host to
  be a server can say so in the panel in one click; baking it into the installer
  would make every panel host a workload host by default, which the vision's
  "the control plane never runs user workloads" argues against as a default even
  though it permits it as a choice.

## Implementation note — the identity read fails closed *(2026-09-07)*

The detection in §6 — *read the agent's own identity file, because matching
hostnames against the fleet would be a guess* — was right about the source of
truth and wrong about what a failed read means.

**What was broken.** `cypherd` runs under `DynamicUser=true`, and the agent
creates its state directory at `0700` owned by root. On the panel's own machine
that read is refused:

```
# ls -ld /var/lib/cypher-agent
drwx------ 3 root root /var/lib/cypher-agent
# setpriv --reuid=63024 cat /var/lib/cypher-agent/identity.json
cat: /var/lib/cypher-agent/identity.json: Permission denied
```

`LocalServerID` returned `("", false)` for that refusal — the same answer it
returns for a machine that has never been joined. So the panel reported
`available`, offered the button on a host already running
`srv_ff5yxbrbznuvlir4fqzxm4bok5`, and `handleCreateLocalServer`'s only
duplicate guard is `state == already_joined`. Each click therefore created
another Server row and another join token.

**Two fixes, and both are needed.**

*The panel fails closed.* `LocalServerID` now returns three values and
distinguishes `fs.ErrNotExist` — which genuinely means "no agent has enrolled
here" — from every other error, which means "cannot tell". The handler maps the
second to a new `unknown` state that offers no button and says why. This is the
half that matters: even if packaging changes again and the read breaks again,
the panel refuses rather than silently duplicating.

*The agent makes the common case readable.* Its state directory is created — and
re-asserted on every start, so an upgrade repairs an existing fleet — at `0711`
instead of `0700`. `--x` is traversal without listing: the filenames are not
enumerable, a name can only be opened if its own mode permits it, and
`agent-key.pem` stays `0600`. What becomes readable is `identity.json` at `0644`,
carrying a server id and two addresses that the panel's UI, its audit log and
its join command all display anyway.

**Why not publish status from the helper instead.** That was the first idea and
it only fixes the machines the helper touched. The failure bites hardest on a
host enrolled the ordinary way — `curl … | sh` with the join command — where no
helper ever ran and no status file exists. The identity file is written by every
enrollment path, which is why it was the right source of truth to begin with.
