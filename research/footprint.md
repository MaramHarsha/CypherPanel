# Whole-stack footprint, measured the same way

`research/dokploy.md` §"Measured baseline footprint" ends with a commitment:

> Our budgets (control plane < 300 MB RSS, agent < 50 MB, no Redis/Node/SSR)
> target roughly **3–4× less memory and ~10× less platform disk**. When Phase 1
> lands, benchmark the **same way** (whole-box on a fresh VPS, same metrics) so
> the comparison is honest.

This is that benchmark. It exists because the README's table was comparing two
different things: **process RSS for `cypherd` and `cypher-agent`** against
**Dokploy's whole-platform measurement**. Both numbers were true and the
comparison was not — a platform is the daemon plus everything the daemon needs
running, and ours needs Docker, Postgres and Traefik exactly as theirs does.

## Method

Same accounting as `research/dokploy.md`: **everything the platform runs,
excluding the operating system and excluding the user's own applications.** Each
process and container is measured by name rather than by reading `free`, so
unrelated things on the host cannot flatter or inflate the number.

- Host processes: `ps -eo rss,comm`, summed per command name.
- Containers: `docker stats --no-stream`.
- Disk: `docker images` for the platform's own images, `du -sh` on the panel's
  Postgres volume, `ls -l` on the two binaries.

## Measured 2026-09-07

**Host:** 7.9 GiB RAM — the same VPS class as the Dokploy measurement (7.76 GiB).

**Conditions, stated because they matter:** this is the project's own live
install, not a fresh VPS. It has deployed applications, run builds, and been up
for the better part of three days. That makes `dockerd` and `containerd` an
**upper bound** rather than a best case — a daemon that has pulled sixteen
images and run a dozen builds holds more than one that has done nothing. The
number below is therefore pessimistic against Dokploy's fresh-install figure,
which is the direction an honest comparison should err in.

### Memory

| Component | RSS |
|---|---|
| `cypherd` (control plane, REST + UI + bus + scheduler) | 46.8 MiB |
| `cypher-agent` | 20.6 MiB |
| PostgreSQL (`cypherpanel-postgres`) | 52.2 MiB |
| Traefik (`cypher-proxy`) | 24.3 MiB |
| Maintenance responder (`cypher-maintenance`, nginx) | 2.0 MiB |
| `dockerd` | 210.2 MiB |
| `containerd` | 66.4 MiB |
| **Platform total** | **≈ 422 MiB** |

Dokploy, measured the same way: **≈ 1 GiB**. So **roughly 2.4× less**, not the
30× the old table implied. That is still the difference between fitting on a
1 GB VPS and not fitting on one, which is the claim that actually matters — but
it is a different claim from the one a reader took away, and the smaller honest
number is the one worth defending.

Two thirds of our figure is **Docker itself** (`dockerd` + `containerd` =
276 MiB), which every one of these platforms requires and none of them controls.
CypherPanel's own share — plane, agent, database, proxy — is **146 MiB**.

### Disk, before the first application

| Component | Size |
|---|---|
| `cypherd` binary | 52 MB |
| `cypher-agent` binary | 22 MB |
| `postgres:16-alpine` | 420 MB |
| `traefik:v3.3` | 286 MB |
| `nginx:1.27-alpine` (maintenance responder) | 74.5 MB |
| `cypherpanel-pgdata` volume | 67 MB |
| **Platform total** | **≈ 0.92 GB** |

Dokploy, measured the same way: **3.84 GB** of images and volumes for a panel
that has deployed nothing. So **roughly 4× less** — close to the "~10× less
platform disk" target, and short of it, which is worth saying rather than
rounding towards.

The Postgres volume is 67 MB on an install carrying real projects, servers,
deployments, revisions and audit history; it is a few megabytes on a fresh one.

## What this measurement does not settle

- **It is not a fresh-VPS run.** The conditions above bias it upward, but a
  clean-room number on a new box is still the one to publish beside Dokploy's,
  and it belongs in the release rehearsal rather than here.
- **Coolify is still not measured whole-box.** Its column is derived from the
  components its stack runs. Until someone installs it and measures it the same
  way, it is an estimate and the README says so.
- **Block I/O is not measured.** `research/dokploy.md` records 22.76 GiB written
  on an idle fresh install and calls it a design warning for us. Whether we
  inherited that churn is a separate measurement, and an unanswered one.
