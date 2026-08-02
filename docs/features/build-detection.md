# Build detection — deploying a repository that has no Dockerfile

> Written 2026-08-02, from a real failure: a static portfolio site
> (`index.html` plus assets, no Dockerfile) could not be deployed, because the
> create form demanded a Dockerfile path and build context for a repository
> that had neither. Vocabulary per [glossary.md](../glossary.md).

## 1. The problem

Until now `build.kind` accepted exactly one value, `dockerfile`, and the create
form asked every operator for a Dockerfile path whether or not their repository
contained one. For the largest single class of things people self-host — a
built front end, a portfolio, a docs site — the honest answer to "where is your
Dockerfile?" is "there isn't one, and I don't want to write one."

## 2. What it does

`build.kind` gains two values. `auto` is the default for new applications.

| kind | behaviour |
|---|---|
| `auto` | Look at the checkout. A Dockerfile at `dockerfile_path` wins. Otherwise an `index.html`/`index.htm` at the context root makes it a static site. Neither → fail, naming both possibilities. |
| `dockerfile` | Build `dockerfile_path`. Unchanged, and still what every existing application uses. |
| `static` | Serve the build context as a website. Fails early if there is no index file. |

A Dockerfile always beats detection: an author who wrote one meant it.

## 3. Where detection runs, and why

**On the builder agent, after clone — never on the control plane.**

The plane does not fetch repositories. It holds no repository credentials
beyond the sealed deploy keys it hands to an agent over mTLS, and ADR-001 keeps
builds off the panel entirely. Detection needs the source tree, so it belongs
where the source tree already is. The plane's only new job is to *carry the
operator's choice*: `BuildWork.build_kind` and `BuildWork.runtime_port`, both
additive fields (ENGINEERING: additive-only wire changes). An agent that
predates them reads an empty `build_kind`, which means `dockerfile` — exactly
its old behaviour.

This also keeps the create form honest. The panel cannot know what is in the
repository at the moment the operator types its URL, so it does not pretend to:
it records "detect this" as desired state and the builder resolves it at build
time, every time, against the commit actually being built.

## 4. The generated image

For a static build the agent writes two files into the throwaway checkout and
builds them with the existing tar-and-build path:

```dockerfile
FROM nginx:1.27-alpine
COPY . /usr/share/nginx/html
RUN rm -f /etc/nginx/conf.d/default.conf \
    /usr/share/nginx/html/Dockerfile.cypherpanel-static \
    /usr/share/nginx/html/nginx.cypherpanel.conf
COPY nginx.cypherpanel.conf /etc/nginx/conf.d/cypherpanel.conf
EXPOSE <runtime.port>
```

Three decisions worth recording, each of which was a bug before it was a
decision:

- **It listens on `runtime.port`, not nginx's default 80.** The route and the
  health check are already configured against that port. A static site that
  only worked when the operator happened to pick 80 would not be automatic.
- **No heredocs.** The agent builds through the daemon's classic `/build`
  endpoint, not BuildKit, and the legacy parser rejects `COPY <<EOF`. The first
  draft used one and would have failed to parse on every static site. A test
  asserts the generated Dockerfile stays free of BuildKit-only syntax.
- **The two generated files are deleted from the served root**, so a visitor
  cannot fetch them.

`try_files $uri $uri/ /index.html` gives a single-page app's client-side routes
a working fallback; a plain multi-page site never reaches it.

## 5. Failure

When `auto` can infer nothing the build fails immediately, before any image
work, with the two things that would fix it:

```
could not work out how to build this repository: no Dockerfile at ./Dockerfile,
and no index.html or index.htm to serve as a static site. Add a Dockerfile, or
set the build context to the directory that contains your site
```

That last clause is the common case for a framework build: the site is real but
it lives in `dist/` or `build/`, and the build context is still the repository
root.

## 6. Deliberately not in scope

**Framework builds** — running `npm run build` and serving the output — are not
here. Detecting a `package.json` is easy; deciding the package manager, the
build command, the output directory, the Node version, and the lockfile
strategy is not, and getting it wrong produces a confusing failure in someone
else's toolchain. A repository that needs a build step should commit its build
output or write a Dockerfile, both of which work today. If real usage shows the
demand, it gets its own spec rather than an accreted guess here.

## 7. Verification

Detection and generation are unit-tested (`agent/builder/detect_test.go`),
including the BuildKit-syntax guard and the port defaulting.

End to end, against a real repository with no Dockerfile
(`index.html` + assets): detection chose `static`; the generated Dockerfile
built with `DOCKER_BUILDKIT=0`; the container served the site on the app's port;
sub-pages resolved; an unknown path fell back to `index.html`; and both
generated files were absent from the image.
