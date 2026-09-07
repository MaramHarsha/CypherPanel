# Compose templates

A catalog entry whose resource is a **Compose Stack** rather than a set of
Applications and Managed Databases.

## 1. The failure this exists to stop

`docs/dev/template-import-report.md` records what the Coolify importer refused
and why. **163 entries were refused for the same structural reason**, in the same
words:

> service "X" waits on "Y"; templates order databases before applications and
> nothing else

It is the single largest refusal category in the import, and it is not a bug in
the importer. The native template schema (ADR-007) resolves to Applications and
Managed Databases, and those have no way to express:

- one application waiting on another's health;
- one application addressing another by a stable name — application containers
  are named per revision (`cypher-app_<id>-rev_<rev>`), so there is no hostname
  a sibling can dial;
- a service with no port, which the schema requires one of;
- anything the schema has no field for, such as `shm_size`.

**OpenClaw is the example that brought this here**, and it hits all four:

```yaml
services:
  openclaw:
    image: "coollabsio/openclaw:2026.2.6"
    environment:
      - BROWSER_CDP_URL=http://browser:9223   # dials a sibling by name
    depends_on:
      browser: { condition: service_healthy } # waits on another service
  browser:
    image: "coollabsio/openclaw-browser:latest"
    shm_size: 2g                              # no schema field
    # no port: it is dialled inside the network, never published
```

Every one of those four things is something **compose does natively**, and this
panel already runs compose files: `compose-stacks.md` exists precisely as "the
honest home ADR-007 named for *I have a compose file and want it run*".

So the gap is not capability. The gap is that the catalog cannot offer one.

## 2. What ships

`TplResources` gains a third kind:

```yaml
resources:
  stacks:
    - name: openclaw
      route: { service: openclaw, port: 8080 }
      compose: |
        services: ...
```

`route` is the same thing a hand-made stack declares: which service answers, on
which port. `compose-stacks.md` §5 already explains why a stack names it rather
than the file's own Traefik labels — the managed Proxy runs the file provider
only (ADR-004), so the plane emits the fragment.

Nothing else about a Compose Stack changes. The file is validated by the
existing `compose.ValidateFile`, which refuses `build:` and `container_name:`
and nothing else, and it converges through the existing reconciler.

## 3. What it does NOT do

**It does not translate the 163.** This makes the shape expressible; turning the
importer loose on those entries is its own piece of work with its own report,
and doing it in the same change would mean shipping 163 templates nobody read.
Hand-curated entries come first, exactly as the original seven did.

**It does not widen what compose may do.** `build:` and `container_name:` stay
refused; `privileged`, host mounts and `cap_add` stay allowed, for the reasons
compose-stacks.md already records. A template is content, and content does not
get a different rulebook from a file an operator pastes in.

**It does not give applications a stable address.** That would be the other way
to solve this — a service alias per application on its environment network — and
it is a bigger, sharper change: it makes revisioned containers addressable,
which interacts with rollout, drain and the zero-downtime swap. It may still be
worth doing. It is not what this needs.

## 4. Interpolation

The same placeholders a template already resolves, resolved in the compose file
too: `{{domain}}`, `{{secret.N}}`, and `{{db.<name>.<field>}}`.

This is what carries Coolify's magic-env convention across. OpenClaw's
`$SERVICE_USER_OPENCLAW` / `$SERVICE_PASSWORD_OPENCLAW` become `{{secret.16}}`
and `{{secret.32}}`; `SERVICE_FQDN_OPENCLAW_8080` becomes `{{domain}}`.

One rule carries over unchanged and matters here: **`{{secret.N}}` resolves
freshly at every use.** The import report refused entries that used one generated
value twice for exactly this reason. A compose file that needs the same secret in
two places must therefore reference it once and let compose's own `${VAR}`
expansion do the rest — the env file the agent writes is where that happens.

## 5. Where the values live

Sealed, in the stack's own env rows, and written by the agent to an env file
`0600` that is removed on every exit path — the mechanism compose-stacks.md §6
already describes. Nothing new holds a secret, and the stored compose file never
contains one.

## 6. Screens

The install dialog does not change shape. It asks for a project, an environment,
a server, a name and — when the template routes — a domain, and it already uses
`DomainField`, so a compose template gets the zone picker and the
already-in-use warning like everything else.

What changes is where the operator lands afterwards: a compose template installs
to the stack's own page rather than an application's. `first_login` works
unchanged, because it is prose about credentials rather than a link to a
resource kind.

## 7. Acceptance

- A template declaring a stack installs, converges, and is routed at its domain.
- A template declaring a stack AND databases still orders databases first.
- `{{domain}}`, `{{secret.N}}` and `{{db.*}}` resolve inside the compose file.
- A compose file that `compose.ValidateFile` refuses fails the template's own
  validation, at load time — a catalog entry that cannot install must not be
  offered.
- A failed install rolls the stack back with everything else.
- OpenClaw installs and serves.
