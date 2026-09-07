package builder

import (
	"errors"
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

// A private repository cloned with no credential is the most common first
// failure, and "git clone failed: exit status 128" told nobody anything. git's
// own message — "could not read Username" — reads like a terminal problem, so
// the builder names the actual cause and the remedy.
func TestACloneThatNeededACredentialSaysSoAndNamesTheRemedy(t *testing.T) {
	gitSaid := "Cloning into 'x'...\nfatal: could not read Username for 'https://github.com': terminal prompts disabled\n"

	got := cloneFailure(gitSaid, false, errors.New("exit status 128"))
	for _, want := range []string{"needs a credential", "deploy key", "GitHub App"} {
		if !strings.Contains(got, want) {
			t.Fatalf("uncredentialled failure = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "exit status") {
		t.Fatalf("the exit status leaked into the operator-facing reason: %q", got)
	}

	// With a credential the advice is different, because the fix is: the key
	// was removed from the repository, not never attached.
	got = cloneFailure("remote: Permission denied\n", true, errors.New("exit status 128"))
	if !strings.Contains(got, "refused") {
		t.Fatalf("credentialled failure = %q", got)
	}

	// Anything that is not an auth failure is git's own to describe, and its
	// full output is already in the build log — inventing a cause would be
	// worse than passing the error through.
	got = cloneFailure("fatal: unable to access: Could not resolve host\n", false, errors.New("exit status 128"))
	if got != "exit status 128" {
		t.Fatalf("a non-auth failure was reinterpreted as %q", got)
	}
}
