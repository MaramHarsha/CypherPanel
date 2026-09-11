package main

import (
	"context"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/templates"
)

// Reference splitting has to match Docker's own defaults exactly: the digest
// this produces is what every panel will pull, so a repository resolved under
// the wrong namespace pins the wrong software.
func TestSplitReference(t *testing.T) {
	cases := []struct {
		ref, host, repo, tag, digest string
	}{
		{"nginx", "docker.io", "library/nginx", "latest", ""},
		{"nginx:1.27", "docker.io", "library/nginx", "1.27", ""},
		{"acme/web:1.2", "docker.io", "acme/web", "1.2", ""},
		{"ghcr.io/acme/web:1.2", "ghcr.io", "acme/web", "1.2", ""},
		{"lscr.io/linuxserver/calibre-web:latest", "lscr.io", "linuxserver/calibre-web", "latest", ""},
		{"registry:5000/acme/web", "registry:5000", "acme/web", "latest", ""},
		{"acme/web@sha256:abc", "docker.io", "acme/web", "latest", "sha256:abc"},
		{"ghcr.io/acme/web:1.2@sha256:abc", "ghcr.io", "acme/web", "1.2", "sha256:abc"},
	}
	for _, c := range cases {
		host, repo, tag, digest := splitReference(c.ref)
		if host != c.host || repo != c.repo || tag != c.tag || digest != c.digest {
			t.Errorf("splitReference(%q) = %q,%q,%q,%q; want %q,%q,%q,%q",
				c.ref, host, repo, tag, digest, c.host, c.repo, c.tag, c.digest)
		}
	}
}

// Docker Hub images keep their familiar short form, so the catalog reads the
// way an operator would write the reference by hand.
func TestCanonicalName(t *testing.T) {
	cases := []struct{ host, repo, want string }{
		{"docker.io", "library/nginx", "nginx"},
		{"docker.io", "acme/web", "acme/web"},
		{"index.docker.io", "library/nginx", "nginx"},
		{"ghcr.io", "acme/web", "ghcr.io/acme/web"},
	}
	for _, c := range cases {
		if got := canonicalName(c.host, c.repo); got != c.want {
			t.Errorf("canonicalName(%q,%q) = %q, want %q", c.host, c.repo, got, c.want)
		}
	}
}

// A tag is a usable version only when it already names one release. `5` and
// `latest` move, so they say nothing a catalog card could display.
func TestTagVersion(t *testing.T) {
	cases := map[string]string{
		"1.2.3":        "1.2.3",
		"v2.10.0":      "2.10.0",
		"1.2.3-alpine": "1.2.3-alpine",
		"latest":       "",
		"5":            "",
		"1.2":          "",
		"stable":       "",
	}
	for tag, want := range cases {
		if got := tagVersion(tag); got != want {
			t.Errorf("tagVersion(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestRegistryOf(t *testing.T) {
	got := registryOf("https://ghcr.io/v2/acme/web/manifests/1.2")
	if got != "ghcr.io" {
		t.Errorf("registryOf = %q, want ghcr.io", got)
	}
}

// A pre-seeded cache stands in for the registry: pinning is exercised without
// a network, and the cache is the same path a real re-run takes.
func pinnedRegistry(entries map[string]*cacheEntry) *registry {
	return newRegistry(&imageCache{entries: entries})
}

// A template whose image cannot be resolved becomes a refusal: shipping a
// mutable reference would let a routine redeploy cross a major version.
func TestApplyPinsRefusesUnresolvableImages(t *testing.T) {
	r := outcome{slug: "example"}
	r.tpl.Resources.Applications = append(r.tpl.Resources.Applications, tplApp("acme/web:1"))
	// An empty cache and a cancelled context: nothing can be resolved.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	applyPins(ctx, &r, pinnedRegistry(map[string]*cacheEntry{}))
	if !containsSubstring(r.reasons, "could not be pinned") {
		t.Fatalf("reasons = %v, want a pinning refusal", r.reasons)
	}
}

func TestApplyPinsTakesVersionFromRoutedApp(t *testing.T) {
	r := outcome{slug: "example"}
	worker := tplApp("acme/worker:1")
	web := tplApp("acme/web:1")
	web.Route = true
	r.tpl.Resources.Applications = append(r.tpl.Resources.Applications, worker, web)
	applyPins(context.Background(), &r, pinnedRegistry(map[string]*cacheEntry{
		"acme/worker:1": {Pinned: "acme/worker@sha256:aaa", Version: "0.9.0"},
		"acme/web:1":    {Pinned: "acme/web@sha256:bbb", Version: "3.1.0"},
	}))
	if len(r.reasons) > 0 {
		t.Fatalf("reasons = %v, want none", r.reasons)
	}
	if r.tpl.Version != "3.1.0" {
		t.Errorf("version = %q, want the routed application's", r.tpl.Version)
	}
	if r.tpl.Resources.Applications[0].Image != "acme/worker@sha256:aaa" {
		t.Errorf("image = %q, want the pinned reference", r.tpl.Resources.Applications[0].Image)
	}
}

// tplApp keeps the pinning tests focused on the reference rewrite rather than
// on assembling a whole valid application.
func tplApp(image string) templates.TplApplication {
	return templates.TplApplication{Name: "app", Image: image, Port: 80}
}

// The port and the digest are resolved by independent lookups, so either can
// land in the cache first. A cached port must not be mistaken for a resolved
// pin — that would ship a template with an empty image reference.
func TestPortOnlyCacheEntryDoesNotSatisfyPinning(t *testing.T) {
	r := outcome{slug: "example"}
	r.tpl.Resources.Applications = append(r.tpl.Resources.Applications, tplApp("acme/web:1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	applyPins(ctx, &r, pinnedRegistry(map[string]*cacheEntry{
		"acme/web:1": {Port: 8080, PortKnown: true},
	}))
	if !containsSubstring(r.reasons, "could not be pinned") {
		t.Fatalf("reasons = %v, want a refusal rather than an empty image", r.reasons)
	}
}
