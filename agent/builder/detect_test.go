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
		got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile")
		if err != nil || got != kindDockerfile {
			t.Fatalf("got %q, %v; want dockerfile", got, err)
		}
	})

	t.Run("index.html and no Dockerfile is a static site", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "index.html")
		got, err := resolveBuildKind(kindAuto, dir, "./Dockerfile")
		if err != nil || got != kindStatic {
			t.Fatalf("got %q, %v; want static", got, err)
		}
	})

	t.Run("neither: say so, and say what to do", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "main.go")
		_, err := resolveBuildKind(kindAuto, dir, "./Dockerfile")
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
	if got, err := resolveBuildKind(kindDockerfile, dir, "./Dockerfile"); err != nil || got != kindDockerfile {
		t.Errorf("explicit dockerfile: got %q, %v", got, err)
	}

	// An empty kind is what pre-existing applications send.
	if got, err := resolveBuildKind("", dir, "./Dockerfile"); err != nil || got != kindDockerfile {
		t.Errorf("empty kind must stay dockerfile for compatibility: got %q, %v", got, err)
	}

	// static without an index is a misconfiguration worth naming early.
	empty := t.TempDir()
	if _, err := resolveBuildKind(kindStatic, empty, "./Dockerfile"); err == nil {
		t.Error("static with no index.html should fail with a clear message")
	}

	if _, err := resolveBuildKind("nixpacks", dir, "./Dockerfile"); err == nil {
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
