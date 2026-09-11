package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// "auto" is what new applications get, so its three outcomes are the contract.
func TestResolveBuildKindAuto(t *testing.T) {
	t.Run("a Dockerfile wins — the author meant it", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Dockerfile")
		writeFile(t, dir, "index.html") // present too; must not win
		got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", nil)
		if err != nil || got != kindDockerfile {
			t.Fatalf("got %q, %v; want dockerfile", got, err)
		}
	})

	t.Run("index.html and no Dockerfile is a static site", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "index.html")
		got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", nil)
		if err != nil || got != kindStatic {
			t.Fatalf("got %q, %v; want static", got, err)
		}
	})

	t.Run("neither: say so, and say what to do", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "main.go")
		_, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", nil)
		if err == nil {
			t.Fatal("want an error when nothing is inferable")
		}
		for _, want := range []string{"Dockerfile", "static site", "build context"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q, got: %v", want, err)
			}
		}
	})
}

// An explicit kind is an instruction, not a hint.
func TestResolveBuildKindExplicit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.html")

	// dockerfile is taken at its word even with no Dockefile present: the
	// build then fails with docker's own error, which is the honest one.
	if got, err := resolveBuildKind(kindDockerfile, dir, "./Dockerfile", nil); err != nil || got != kindDockerfile {
		t.Errorf("explicit dockerfile: got %q, %v", got, err)
	}

	// An empty kind is what pre-existing applications send.
	if got, err := resolveBuildKind("", dir, "./Dockerfile", nil); err != nil || got != kindDockerfile {
		t.Errorf("empty kind must stay dockerfile for compatibility: got %q, %v", got, err)
	}

	// static without an index is a misconfiguration worth naming early.
	empty := t.TempDir()
	if _, err := resolveBuildKind(kindStatic, empty, "./Dockerfile", nil); err == nil {
		t.Error("static with no index.html should fail with a clear message")
	}

	if _, err := resolveBuildKind("nixpacks", dir, "./Dockerfile", nil); err == nil {
		t.Error("an unknown kind must be rejected")
	}
}

// The generated image has to listen where the route and health check already
// point, not on nginx's default 80.
func TestStaticDockerfileListensOnTheAppPort(t *testing.T) {
	df := staticDockerfile(3000)
	if !strings.Contains(staticNginxConf(3000), "listen 3000;") {
		t.Errorf("should listen on the app's port:\n%s", df)
	}
	if strings.Contains(staticNginxConf(3000), "listen 80;") {
		t.Error("must not fall back to nginx's default port")
	}
	// SPA routes must resolve rather than 404.
	if !strings.Contains(staticNginxConf(3000), "try_files") {
		t.Error("expected an index.html fallback for client-side routes")
	}
	// A zero port would emit `listen 0;`, which nginx rejects.
	dir := t.TempDir()
	if _, err := writeStaticBuild(dir, 0); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(dir, staticNginxConfName))
	if !strings.Contains(string(conf), "listen 8080;") {
		t.Errorf("a zero port should fall back to 8080, got:\n%s", conf)
	}
}

func TestWriteStaticBuild(t *testing.T) {
	dir := t.TempDir()
	name, err := writeStaticBuild(dir, 8080)
	if err != nil {
		t.Fatal(err)
	}
	// The name must be relative: it is passed to docker as -f against the
	// context, and an absolute host path would not resolve inside the tar.
	if filepath.IsAbs(name) {
		t.Errorf("generated dockerfile path must be context-relative, got %q", name)
	}
	for _, f := range []string{name, staticNginxConfName} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("generated %s not written: %v", f, err)
		}
	}
}

// The agent builds through the daemon's classic /build endpoint, not BuildKit.
// The legacy parser rejects heredocs, so one in the generated Dockerfile would
// fail at parse time on every static site.
func TestStaticDockerfileAvoidsBuildKitOnlySyntax(t *testing.T) {
	df := staticDockerfile(8080)
	if strings.Contains(df, "<<") {
		t.Errorf("heredoc requires BuildKit; the classic builder cannot parse it:\n%s", df)
	}
	if strings.Contains(df, "--mount") || strings.Contains(df, "# syntax=") {
		t.Errorf("BuildKit-only directive in a classic build:\n%s", df)
	}
	// The generated files must not remain publicly served.
	if !strings.Contains(df, "rm -f") || !strings.Contains(df, staticNginxConfName) {
		t.Errorf("generated files should be removed from the served root:\n%s", df)
	}
}

// ─── pack builds (pack-builds.md §4) ────────────────────────────────────────

func writeFiles(t *testing.T, dir string, names ...string) string {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", n, err)
		}
	}
	return dir
}

// A language manifest makes `auto` reach for the pack — but only where the
// pack is actually installed.
func TestAutoPrefersThePackForAnApplication(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json")

	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindNixpacks: true})
	if err != nil || got != kindNixpacks {
		t.Fatalf("got %q, %v; want nixpacks", got, err)
	}
}

// The behaviour change worth being explicit about: a Vite repository has BOTH
// a package.json and an index.html, and serving its SOURCE index.html ships
// unbuilt TypeScript no browser can run.
func TestAutoPrefersThePackOverServingUnbuiltSource(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json", "index.html")

	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindNixpacks: true})
	if err != nil || got != kindNixpacks {
		t.Fatalf("got %q, %v; want nixpacks for a repository that needs building", got, err)
	}
}

// A builder that has not installed the pack must not start failing builds it
// used to complete: detection falls through exactly as before.
func TestWithoutThePackAutoBehavesAsItAlwaysDid(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json", "index.html")

	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", nil)
	if err != nil || got != kindStatic {
		t.Fatalf("got %q, %v; want static where no pack is installed", got, err)
	}
}

// A Dockerfile still wins: an author who wrote one meant it.
func TestADockerfileStillBeatsThePack(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json", "Dockerfile")

	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindNixpacks: true})
	if err != nil || got != kindDockerfile {
		t.Fatalf("got %q, %v; want dockerfile", got, err)
	}
}

// A directory of files to serve is not an application a pack should claim.
func TestAPlainSiteIsStillStatic(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "index.html", "style.css")

	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindNixpacks: true})
	if err != nil || got != kindStatic {
		t.Fatalf("got %q, %v; want static", got, err)
	}
}

// Chosen explicitly it is an assertion, so a missing pack is a failure that
// says what to do rather than a silent fall back to something else.
func TestExplicitNixpacksWithoutTheBinaryFailsLoudly(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json")

	_, err := resolveBuildKind(kindNixpacks, dir, "./Dockerfile", nil)
	if err == nil {
		t.Fatal("resolveBuildKind succeeded with no pack installed")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want it to name the missing binary", err)
	}
}

func TestExplicitNixpacksIsHonouredWhenAvailable(t *testing.T) {
	// No manifest at all: an explicit choice does not need detection to agree,
	// because the operator has already said how this repository builds.
	got, err := resolveBuildKind(kindNixpacks, t.TempDir(), "./Dockerfile", map[string]bool{kindNixpacks: true})
	if err != nil || got != kindNixpacks {
		t.Fatalf("got %q, %v; want nixpacks", got, err)
	}
}

// Every manifest in the list has to actually be recognised, or a language
// silently falls through to "cannot infer".
func TestEveryLanguageManifestIsRecognised(t *testing.T) {
	for _, name := range languageManifests {
		dir := writeFiles(t, t.TempDir(), name)
		got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindNixpacks: true})
		if err != nil || got != kindNixpacks {
			t.Errorf("%s: got %q, %v; want nixpacks", name, got, err)
		}
	}
	// A .csproj is matched by glob, because its name is the project's.
	dir := writeFiles(t, t.TempDir(), "Web.csproj")
	if got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindNixpacks: true}); err != nil || got != kindNixpacks {
		t.Errorf(".csproj: got %q, %v; want nixpacks", got, err)
	}
}

// ─── Railpack (pack-builds.md §§2-4) ────────────────────────────────────────

// Both halves are required, and the refusal says so: the binary without buildx
// is exactly as unusable as buildx without the binary.
func TestExplicitRailpackNeedsBothHalves(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json")

	_, err := resolveBuildKind(kindRailpack, dir, "./Dockerfile", map[string]bool{kindRailpack: false})
	if err == nil {
		t.Fatal("resolveBuildKind succeeded with Railpack unavailable")
	}
	if !strings.Contains(err.Error(), "buildx") || !strings.Contains(err.Error(), "railpack") {
		t.Fatalf("err = %v, want it to name both halves", err)
	}
}

func TestExplicitRailpackIsHonouredWhenAvailable(t *testing.T) {
	got, err := resolveBuildKind(kindRailpack, t.TempDir(), "./Dockerfile", map[string]bool{kindRailpack: true})
	if err != nil || got != kindRailpack {
		t.Fatalf("got %q, %v; want railpack", got, err)
	}
}

// `auto` never infers Railpack: choosing between two packs that claim the same
// repositories would be arbitrary, and Nixpacks needs no BuildKit and pulls no
// frontend image.
func TestAutoNeverInfersRailpack(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json", "index.html")

	// Only Railpack is installed — detection must still not choose it.
	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile", map[string]bool{kindRailpack: true})
	if err != nil || got != kindStatic {
		t.Fatalf("got %q, %v; want static — auto must not infer railpack", got, err)
	}
}

// With both installed, `auto` still picks Nixpacks.
func TestAutoPrefersNixpacksWhenBothArePresent(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), "package.json")

	got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile",
		map[string]bool{kindNixpacks: true, kindRailpack: true})
	if err != nil || got != kindNixpacks {
		t.Fatalf("got %q, %v; want nixpacks", got, err)
	}
}
