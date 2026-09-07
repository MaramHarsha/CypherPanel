package applications

import "testing"

// The shapes an operator actually types, and what each must become.
//
// The schemeless form is the one that broke a real deploy: the create dialog
// suggested `github.com/acme/web`, git read it as a local directory, and the
// build died with `exit status 128`.
func TestGitRemoteNormalisesWhatPeopleType(t *testing.T) {
	ok := map[string]string{
		"github.com/acme/web": "https://github.com/acme/web",
		// An absolute path is a remote git can clone, and the deploy
		// integration suite depends on exactly this: it builds a repo in
		// $RUNNER_TEMP and points an application at the directory.
		"/home/runner/work/_temp/app-src": "/home/runner/work/_temp/app-src",
		"/srv/mirrors/web.git":            "/srv/mirrors/web.git",
		"  github.com/acme/web  ":         "https://github.com/acme/web",
		"gitlab.example.com:8443/a/b":     "https://gitlab.example.com:8443/a/b",
		"https://github.com/acme/web":     "https://github.com/acme/web",
		"https://github.com/acme/web.git": "https://github.com/acme/web.git",
		"http://gitea.local/acme/web":     "http://gitea.local/acme/web",
		"ssh://git@github.com/acme/web":   "ssh://git@github.com/acme/web",
		"git://example.com/acme/web":      "git://example.com/acme/web",
		"git@github.com:acme/web.git":     "git@github.com:acme/web.git",
		"file:///srv/mirrors/web.git":     "file:///srv/mirrors/web.git",
	}
	for in, want := range ok {
		got, err := gitRemote(in)
		if err != nil {
			t.Errorf("gitRemote(%q) refused it: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("gitRemote(%q) = %q, want %q", in, got, want)
		}
	}

	// Each of these would be handed to git as a local path.
	for _, bad := range []string{
		"acme/web",   // no host — the ambiguous shorthand, deliberately refused
		"web",        //
		"../web",     //
		"github.com", // a host with no repository
		"https://",   // a scheme with no repository after it
		"my repo",    // a space is not a remote
	} {
		if got, err := gitRemote(bad); err == nil {
			t.Errorf("gitRemote(%q) accepted it as %q — git would treat that as a local directory", bad, got)
		}
	}
}
