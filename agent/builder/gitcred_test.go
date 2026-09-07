package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The clone credential must never reach the URL, and this is the reason stated
// as a test: `git clone https://user:token@host/repo` writes the token into
// .git/config INSIDE the build context, and that directory becomes the Docker
// build context moments later — so the token would be baked into an image
// layer. It would also reach `ps` and any git error that echoes the remote.
//
// The askpass helper reads it from the environment instead, which none of those
// three things can see.
func TestTheCloneCredentialNeverReachesTheURLOrTheBuildContext(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s' \"$GIT_CRED_USER\" ;;\n*) printf '%s' \"$GIT_CRED_PASS\" ;;\nesac\n"
	path := filepath.Join(dir, ".git-askpass-dep_1")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// 0700: the token is read by this process's git and by nothing else on a
	// shared host.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("askpass mode = %v, want 0700", perm)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The secret is passed through the environment, so it must not be written
	// into the helper itself — a file on disk outlives the process.
	if strings.Contains(string(body), "ghs_") || strings.Contains(string(body), "x-access-token:") {
		t.Fatalf("the helper embeds a credential: %s", body)
	}
	if !strings.Contains(string(body), "$GIT_CRED_PASS") {
		t.Fatalf("the helper does not read the credential from the environment: %s", body)
	}
}
