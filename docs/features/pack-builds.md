# Feature spec: Pack builds (Nixpacks and Railpack)

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

Both ship, and they are **not** the same integration — the difference is what
each produces, and it is the reason this feature adds a second way to build.

| Pack | Produces | Built by |
|---|---|---|
| **Nixpacks** | a Dockerfile. `nixpacks build <dir> --out <dir>` writes `.nixpacks/Dockerfile` without building. | the existing tar-and-build path, unchanged |
| **Railpack** | a BuildKit **gateway frontend plan**. `railpack prepare <dir> --plan-out railpack-plan.json` writes an LLB plan its own frontend interprets. | a second transport: `docker buildx build` |

Railpack's plan is not a Dockerfile and cannot be made into one. A gateway
frontend is an image BuildKit fetches and hands the plan to, and the daemon's
classic `/build` endpoint — which every build here uses — has no concept of one.
So supporting Railpack means the agent gains a second way to make an image.

That is a real cost and it is worth naming rather than absorbing quietly: a
Railpack build needs `docker buildx` on the builder as well as the `railpack`
binary, and it pulls a frontend image from `ghcr.io` on first use. What makes
two transports acceptable is that nothing downstream can tell them apart — both
produce the same `cypher/<app>:<revision>` tag with the same management labels,
and rollout, relay, rollback and garbage collection are identical either way.

The invocation is Railpack's own published contract for platforms, taken from
its frontend reference rather than inferred:

```sh
railpack prepare <context> --plan-out <context>/railpack-plan.json

docker buildx build \
  --build-arg BUILDKIT_SYNTAX=ghcr.io/railwayapp/railpack-frontend \
  --file railpack-plan.json --tag <image> --load --progress plain \
  --label <management labels> .
```

`--load` is explicit: with buildx's container driver the result stays in the
builder unless asked for, and an image that never reached the daemon would fail
at rollout with nothing to explain it.

The same transport is what BuildKit cache mounts and multi-arch builds need
([feature matrix](../product/feature-matrix.md): "Multi-arch image builds",
V1.x). It exists now, so those become configuration rather than architecture.

## 3. The build kinds

`build.kind` gains two values beside `auto`, `dockerfile` and `static`:

```
nixpacks  Hand the checkout to Nixpacks. It writes a Dockerfile; we build it.
railpack  Hand the checkout to Railpack. It writes a frontend plan; BuildKit builds it.
```

Chosen explicitly, either is an assertion: fail loudly if the pack cannot plan
the repository, because the operator said this is how it builds.

`railpack` requires **both** halves, and the refusal says so — the binary
without `buildx` is exactly as unusable as `buildx` without the binary, and a
message naming only one would send an operator to fix the wrong thing.

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

**`auto` never infers `railpack`.** Both packs claim the same repositories, so
choosing between them by detection would be arbitrary — and the tie-break that
does exist favours Nixpacks: it needs no BuildKit and pulls no frontend image.
Railpack is a deliberate choice an operator makes, which is also what makes its
extra requirements fair.

## 5. How it runs

**Nixpacks:**

```
nixpacks build <context> --out <context> --name <image tag>
```

`--out` is what makes this fit: Nixpacks writes `.nixpacks/Dockerfile` plus
whatever it needs beside it and **does not build**. The agent then runs its
ordinary path — tar the context, `POST /build` with `dockerfile=.nixpacks/Dockerfile`
— so labels are stamped in one place, a private base image's credential is
applied in one place ([registries.md](registries.md)), and build logs stream
from one place.

**Railpack** takes the second transport, shown in §2. A pack declares which
shape it produced and the builder picks the path; nothing else in the build
knows there is a choice.

One thing the second transport does NOT carry today: the private-base-image
credential. `X-Registry-Config` is a header on the classic endpoint, and buildx
reads registry credentials from the Docker CLI config instead. A Railpack build
whose base image is private therefore needs a `docker login` on the builder —
stated here rather than discovered, and the reason `registries.md`'s per-work
credential remains the better path for anything that can use a Dockerfile.

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

- **Installing a pack for the operator.** The agent installer is deliberately
  small (`curl | sh`, Docker, the binary). A pack is an opt-in the operator adds
  to a builder, and `auto` is written so that not adding one costs nothing.
- **Per-work registry credentials on the BuildKit transport.** §5. It needs a
  buildx-shaped credential mechanism rather than the classic endpoint's header.
- **Multi-arch and cache mounts.** The transport that makes them possible now
  exists; exposing them is their own feature.
- **Build-time variables from the panel.** §5: the mechanism Nixpacks offers
  leaks values through argv, so this needs a design of its own rather than the
  obvious one.
- **Other packs** (Paketo, Heroku buildpacks). The same shape would fit — a
  pack declares what it produced and the builder picks a transport — but none
  has the demand that moved these two.
