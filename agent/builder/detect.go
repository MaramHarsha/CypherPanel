// Build-kind detection. This lives on the builder, not the control plane,
// because it is the only place the source actually exists: the plane never
// clones a repository (ADR-001 — no builds on the panel, and no repo
// credentials outside the agent).
package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Build kinds, mirroring core/domain. Duplicated rather than imported: the
// agent module does not depend on core, and these three strings are the wire
// contract (work.proto BuildWork.build_kind), not shared code.
const (
	kindDockerfile = "dockerfile"
	kindStatic     = "static"
	kindAuto       = "auto"
	// kindNixpacks and kindRailpack hand the checkout to a build pack, which
	// works out the language, package manager, build command and runtime
	// (pack-builds.md). They differ in what they produce — a Dockerfile and a
	// BuildKit frontend plan — which is why the builder has two transports.
	kindNixpacks = "nixpacks"
	kindRailpack = "railpack"
)

// languageManifests are the files that mean "this repository is an application
// a pack can build", as opposed to a directory of files to serve. Presence of
// one is what makes `auto` reach for Nixpacks before it considers a static
// site — a Vite repository has both, and serving its SOURCE index.html ships
// unbuilt TypeScript that no browser can run (pack-builds.md §4).
var languageManifests = []string{
	"package.json",
	"requirements.txt",
	"pyproject.toml",
	"Pipfile",
	"go.mod",
	"Gemfile",
	"Cargo.toml",
	"composer.json",
	"pom.xml",
	"build.gradle",
	"build.gradle.kts",
	"mix.exs",
}

// staticIndexNames are the entry points that make a directory a website. A
// repository with one of these at the root of the build context and no
// Dockerfile is a static site — the case the operator should never have to
// explain to us.
var staticIndexNames = []string{"index.html", "index.htm"}

// resolveBuildKind decides how to build the checkout in contextDir.
//
// "auto" is the interesting one: a Dockerfile wins if it is there, because an
// author who wrote one meant it. Then a language manifest makes it a pack
// build, then an index file makes it a static site. Anything else is a build we
// cannot infer, and saying so plainly beats guessing and failing later with a
// confusing error.
//
// packs reports which build packs are actually usable here, and it is part of
// the condition on purpose (pack-builds.md §4): on a builder with none,
// detection falls through to static exactly as it did before packs existed, so
// a node that has not opted in does not start failing builds it used to
// complete.
func resolveBuildKind(declared, contextDir, dockerfilePath string, packs map[string]bool) (string, error) {
	switch declared {
	case kindDockerfile, "":
		return kindDockerfile, nil
	case kindStatic:
		if !hasStaticIndex(contextDir) {
			return "", fmt.Errorf(
				"this app is set to build as a static site, but no %s was found in %s — "+
					"point the build context at the directory holding your index.html",
				strings.Join(staticIndexNames, " or "), describeContext(contextDir))
		}
		return kindStatic, nil
	case kindNixpacks:
		// Chosen explicitly, it is an assertion: the operator said this is how
		// the repository builds, so a missing pack is a failure rather than a
		// silent fall back to something else.
		if !packs[kindNixpacks] {
			return "", fmt.Errorf(
				"this app is set to build with Nixpacks, but the nixpacks binary is not installed on this builder — " +
					"install it, or set the build to auto or dockerfile")
		}
		return kindNixpacks, nil
	case kindRailpack:
		// Railpack needs BOTH halves, and the message says so: its output is a
		// BuildKit frontend plan, so the binary without buildx is as unusable
		// as buildx without the binary.
		if !packs[kindRailpack] {
			return "", fmt.Errorf(
				"this app is set to build with Railpack, but this builder is missing the railpack binary " +
					"or docker buildx — Railpack builds need both, because its output is a BuildKit frontend plan")
		}
		return kindRailpack, nil
	case kindAuto:
		if hasDockerfile(contextDir, dockerfilePath) {
			return kindDockerfile, nil
		}
		// Nixpacks is `auto`'s pack, and Railpack is never inferred: choosing
		// between two packs that claim the same repositories would be
		// arbitrary, and Nixpacks is the one that needs no BuildKit and pulls
		// no frontend image. Railpack is a deliberate choice (§4).
		if packs[kindNixpacks] && hasLanguageManifest(contextDir) {
			return kindNixpacks, nil
		}
		if hasStaticIndex(contextDir) {
			return kindStatic, nil
		}
		return "", fmt.Errorf(
			"could not work out how to build this repository: no %s at %s, and no %s to serve as a static site. "+
				"Add a Dockerfile, or set the build context to the directory that contains your site",
			"Dockerfile", dockerfilePath, strings.Join(staticIndexNames, " or "))
	default:
		return "", fmt.Errorf("unknown build kind %q", declared)
	}
}

// hasLanguageManifest reports whether the context looks like an application a
// pack can build. A .csproj is matched by glob because its name is the
// project's, not a convention.
func hasLanguageManifest(contextDir string) bool {
	for _, name := range languageManifests {
		if st, err := os.Stat(filepath.Join(contextDir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	matches, err := filepath.Glob(filepath.Join(contextDir, "*.csproj"))
	return err == nil && len(matches) > 0
}

func hasDockerfile(contextDir, dockerfilePath string) bool {
	if dockerfilePath == "" {
		dockerfilePath = "./Dockerfile"
	}
	// dockerfile_path is relative to the build context, the same way the
	// docker CLI treats -f against its context argument.
	st, err := os.Stat(filepath.Join(contextDir, filepath.Clean(dockerfilePath)))
	return err == nil && !st.IsDir()
}

func hasStaticIndex(contextDir string) bool {
	for _, name := range staticIndexNames {
		if st, err := os.Stat(filepath.Join(contextDir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func describeContext(contextDir string) string {
	if base := filepath.Base(contextDir); base != "." && base != string(filepath.Separator) {
		return base + "/"
	}
	return "the repository root"
}

// The synthesized static build.
//
// Two files are written into the checkout — a Dockerfile and an nginx server
// block — and both are deleted from the served directory inside the image, so
// a visitor cannot fetch them.
//
// Deliberately no heredoc (`COPY <<EOF`): the agent builds through the daemon's
// classic /build endpoint, not BuildKit, and the legacy parser does not
// understand them. The build would fail at parse time on every static site.
//
// nginx listens on the app's own port rather than its default 80, because the
// route and health check were already configured against that port — a static
// site that only worked when the operator happened to choose 80 would not be
// "it just works". try_files falls back to index.html so a single-page app's
// client-side routes resolve; a plain site never reaches the fallback.
const (
	staticDockerfileName = "Dockerfile.cypherpanel-static"
	staticNginxConfName  = "nginx.cypherpanel.conf"
)

func staticNginxConf(port uint32) string {
	return fmt.Sprintf(`server {
    listen %d;
    server_name _;
    root /usr/share/nginx/html;
    index index.html index.htm;
    location / {
        try_files $uri $uri/ /index.html;
    }
}
`, port)
}

func staticDockerfile(port uint32) string {
	return fmt.Sprintf(`FROM nginx:1.27-alpine
COPY . /usr/share/nginx/html
RUN rm -f /etc/nginx/conf.d/default.conf \
    /usr/share/nginx/html/%s \
    /usr/share/nginx/html/%s
COPY %s /etc/nginx/conf.d/cypherpanel.conf
EXPOSE %d
`, staticDockerfileName, staticNginxConfName, staticNginxConfName, port)
}

// writeStaticBuild materializes the synthesized build into the context so the
// existing tar-and-build path carries it unchanged. Returns the Dockerfile path
// relative to the context, which is what the daemon expects for -f.
func writeStaticBuild(contextDir string, port uint32) (string, error) {
	if port == 0 {
		port = 8080 // a zero would emit `listen 0;`, which nginx rejects
	}
	files := map[string]string{
		staticNginxConfName:  staticNginxConf(port),
		staticDockerfileName: staticDockerfile(port),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(contextDir, name), []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("writing generated %s: %w", name, err)
		}
	}
	return staticDockerfileName, nil
}
