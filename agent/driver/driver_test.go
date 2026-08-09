package driver

import (
	"strings"
	"testing"
)

// The marker is the only durable record that a registry reference is ours to
// remove, and garbage collection reads it back with no spec and no container
// to check it against — so the reference has to survive the round trip
// byte-for-byte, whatever it contains.
func TestPullMarkerRoundTrip(t *testing.T) {
	for _, source := range []string{
		"ghost:5",
		"gitea/gitea:1.23.8",
		"ghcr.io/org/app:v1.2.3",
		"registry.example.com:5000/team/app:RC-1", // uppercase is legal in a tag
		"ghost@sha256:" + strings.Repeat("a", 64), // a digest reference
	} {
		ref, ok := PullMarkerRef("app1", source)
		if !ok {
			t.Fatalf("PullMarkerRef(%q) refused a reference well inside the limit", source)
		}
		if !strings.HasPrefix(ref, PullMarkerPrefix) {
			t.Fatalf("marker %q is outside the managed namespace", ref)
		}
		// Docker's own constraints: one repository path component and a tag.
		name, tag, found := strings.Cut(ref, ":")
		if !found || tag != "app1" || len(name) > maxReferenceName {
			t.Fatalf("marker %q is not a legal <name>:<tag> within the length limit", ref)
		}
		if encoded := strings.TrimPrefix(name, PullMarkerPrefix); strings.Trim(encoded, "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("encoded reference %q uses characters a repository name forbids", encoded)
		}

		appID, got, ok := ParsePullMarker(ref)
		if !ok || appID != "app1" || got != source {
			t.Fatalf("ParsePullMarker(%q) = %q, %q, %v; want app1, %q, true", ref, appID, got, ok, source)
		}
	}
}

// Anything that is not one of ours must not be read as a marker: GC removes
// what a marker names, so a false positive would untag an operator's image.
func TestParsePullMarkerRejectsForeignReferences(t *testing.T) {
	for _, ref := range []string{
		"ghost:5",
		"cypher/app1:rev1",             // a managed alias, not a marker
		"cypher-pull/notbase32!:app1",  // undecodable
		"cypher-pull/mzxw6:",           // no application
		"cypher-pull/:app1",            // nothing encoded
		"cypher-pull/mzxw6",            // no tag at all
		"cypher-pullish/mzxw6ytb:app1", // a namespace that merely looks like ours
	} {
		if _, _, ok := ParsePullMarker(ref); ok {
			t.Errorf("ParsePullMarker(%q) claimed a reference that is not ours", ref)
		}
	}
}

// A reference too long to encode within Docker's name limit is refused rather
// than truncated: half a reference names the wrong image, and GC acts on what
// a marker says without a second opinion.
func TestPullMarkerRefusesWhatItCannotRecordExactly(t *testing.T) {
	if _, ok := PullMarkerRef("app1", strings.Repeat("a", 200)+":1"); ok {
		t.Fatal("encoded a reference past the repository-name limit")
	}
	if _, ok := PullMarkerRef("", "ghost:5"); ok {
		t.Fatal("built a marker with no application to attribute it to")
	}
	if _, ok := PullMarkerRef("app:1", "ghost:5"); ok {
		t.Fatal("built a marker whose tag cannot hold the application id")
	}
}
