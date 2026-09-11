package builder

import "testing"

// A deploy key is an SSH credential, so GitHub HTTPS URLs are rewritten to
// their SSH form before cloning; everything else passes through untouched
// (deploy-key-private-repos.md §4).
func TestSSHCloneURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/web":     "git@github.com:acme/web.git",
		"https://github.com/acme/web.git": "git@github.com:acme/web.git",
		"git@github.com:acme/web.git":     "git@github.com:acme/web.git",
		"https://gitlab.com/acme/web":     "https://gitlab.com/acme/web",
		"ssh://git@github.com/acme/web":   "ssh://git@github.com/acme/web",
	}
	for in, want := range cases {
		if got := sshCloneURL(in); got != want {
			t.Errorf("sshCloneURL(%q) = %q, want %q", in, got, want)
		}
	}
}
