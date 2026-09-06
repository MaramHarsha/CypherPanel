# Feature spec: Pack builds (Nixpacks)

> A build kind that hands a repository with no Dockerfile to a build pack,
> which works out the language, the package manager, the build command and the
> runtime for us. This is what
> [build-detection.md](build-detection.md) §6 deferred, on the condition it
> named: "if real usage shows the demand, it gets its own spec rather than an
> accreted guess here."
>
> Written 2026-09-06, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. Why a pack, rather than our own heuristics

`build-detection.md` §6 refused to guess at framework builds, and the reasoning
still stands: "detecting a `package.json` is easy; deciding the package
manager, the build command, the output directory, the Node version, and the
lockfile strategy is not, and getting it wrong produces a confusing failure in
someone else's toolchain."

Nothing about that has changed. What changes is who does the deciding. A build
pack is a program whose entire job is that decision, maintained by people
watching every ecosystem's conventions move. Delegating to one is not the
accreted guess §6 refused — it is the alternative to it.

## 2. What ships, and what does not

| Pack | State | Why |
|---|---|---|
| **Nixpacks** | **implemented** | It emits a Dockerfile. `nixpacks build <dir> --out <dir>` writes `.nixpacks/Dockerfile` and the files it needs, without building — so the existing tar-and-build path consumes it unchanged. |
| **Railpack** | **not implemented; blocked on a second build transport** | It is a BuildKit *frontend*: it produces an LLB plan consumed by `ghcr.io/railwayapp/railpack`, invoked through a `# syntax=` directive. The agent builds through the daemon's **classic `/build` endpoint**, which cannot resolve a custom frontend. |

Railpack's blocker is architectural, not incidental, and it is worth stating
precisely because it looks like "one more build kind" and is not. Supporting it
means the agent gains a second way to build — either a BuildKit client session
over `/build?version=2`, or shelling out to `docker buildx build` — alongside
the endpoint every build uses today. That is its own decision, with its own
consequences for what must be installed on a builder, and it should be made in
its own spec rather than smuggled in here. **This spec deliberately leaves
Railpack out rather than shipping an invocation nobody has verified against a
real host.**

The same second transport is what BuildKit cache mounts and multi-arch builds
need ([feature matrix](../product/feature-matrix.md): "Multi-arch image
builds", V1.x), so it is likely to be worth doing once, for three reasons at
once.

## 3. The build kind

`build.kind` gains a fourth value, `nixpacks`, beside `auto`, `dockerfile` and
`static`.

```
nixpacks  Hand the checkout to Nixpacks. It writes a Dockerfile; we build it.
```

Chosen explicitly, it is an assertion: fail loudly if Nixpacks cannot plan the
repository, because the operator said this is how it builds.

## 4. `auto`, and the one behaviour change

`auto`'s order becomes:

1. **A Dockerfile wins.** Unchanged, and for the unchanged reason: an author who
   wrote one meant it.
2. **A language manifest, and Nixpacks available → `nixpacks`.** `package.json`,
   `requirements.txt`, `pyproject.toml`, `go.mod`, `Gemfile`, `Cargo.toml`,
   `composer.json`, `pom.xml`, `build.gradle`, `mix.exs`, `*.csproj`.
3. **An index file → `static`.** Unchanged.
4. Otherwise fail, naming what would fix it. Unchanged.

Step 2 is a real behaviour change and worth being explicit about: a repository
with **both** a `package.json` and an `index.html` — a Vite or Svelte app, say —
used to resolve to `static` and now resolves to `nixpacks`. That is a fix, not a
regression: serving a Vite repository's *source* `index.html` ships unbuilt
TypeScript and a `<script src="/src/main.ts">` no browser can run. It looked
like a successful deploy and was not.

**Availability is part of the condition, deliberately.** Step 2 only fires when
the `nixpacks` binary is actually on the builder. Without it, detection falls
through to step 3 and every existing `auto` application resolves exactly as it
does today. A node that has not installed the pack does not start failing
builds it used to complete — the worst possible outcome for a detection change.

## 5. How it runs

```
nixpacks build <context> --out <context> --name <image tag>
```

`--out` is what makes this fit: Nixpacks writes `.nixpacks/Dockerfile` plus
whatever it needs beside it and **does not build**. The agent then runs its
ordinary path — tar the context, `POST /build` with `dockerfile=.nixpacks/Dockerfile`
— so there is one build path, one place labels are stamped, one place a private
base image's credential is applied ([registries.md](registries.md)), and one
place build logs are streamed from.

**The application's environment variables are deliberately NOT passed to the
pack**, and this is the one place the obvious design is wrong. Nixpacks takes
build-time variables as `--env KEY=VALUE` command-line arguments, and argv is
world-readable through `ps` on the builder: every value would be visible to any
local user for the length of the build. A sealed variable that the rollout
injects over mTLS must not be handed to the process table on the way past.

The pack's configuration therefore comes from `nixpacks.toml` in the repository,
which is where a build's configuration belongs anyway — beside the code it
builds, versioned with it. Build-time variables that genuinely need a value from
the panel (a `NEXT_PUBLIC_*`, say) are a separate feature with a separate
design, because the safe mechanism is a file the pack reads rather than an
argument list, and that decision deserves to be made on its own.

Nixpacks absent when the kind was chosen explicitly is reported as exactly
that — a message naming the missing binary — rather than an opaque exec
failure, the same courtesy `docker compose` gets in
[compose-stacks.md](compose-stacks.md) §4.

## 6. What this does not change

- **Where detection runs.** Still on the builder, after clone, for
  build-detection.md §3's reason: the plane never fetches a repository.
- **The wire.** `build_kind` is an existing string field; `nixpacks` is a new
  value in it, not a new field. An agent that predates this reads a kind it does
  not know and fails that build loudly rather than guessing — which is what it
  already does for any unknown kind.
- **The image.** A pack build produces the same `cypher/<app>:<revision>` tag,
  with the same management labels, and is rolled out, relayed, rolled back and
  garbage-collected exactly like any other.

## 7. Deliberately out of scope

- **Railpack.** §2. It needs the second build transport.
- **Installing Nixpacks for the operator.** The agent installer is deliberately
  small (`curl | sh`, Docker, the binary). A pack is an opt-in the operator adds
  to a builder, and `auto` is written so that not adding it costs nothing.
- **Build-time variables from the panel.** §5: the mechanism Nixpacks offers
  leaks values through argv, so this needs a design of its own rather than the
  obvious one.
- **Other packs** (Paketo, Heroku buildpacks). The same shape would fit; none
  has the demand that moved this one.
