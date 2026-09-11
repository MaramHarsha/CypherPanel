package main

import (
	"encoding/json"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/upgrade"
)

// The panel's upgrade refuses a release.json whose version is not the tag it
// asked for, and reads the agent floor out of it — so this must round-trip
// through the same struct the panel parses.
func TestManifestRoundTripsThroughThePanelsParser(t *testing.T) {
	body, err := Build("v0.1.0", "2026-09-08T00:00:00Z")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var m upgrade.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("the panel could not parse what the release wrote: %v", err)
	}
	if m.Version != "v0.1.0" || m.AgentMinVersion != upgrade.AgentMinVersion || m.SchemaVersion < 55 {
		t.Fatalf("manifest = %+v", m)
	}
	// Byte-identical on a second run: the signer rebuilds and compares.
	again, _ := Build("v0.1.0", "2026-09-08T00:00:00Z")
	if string(again) != string(body) {
		t.Fatal("two builds of the same manifest differ")
	}
}

func TestManifestRefusesWhatCannotBeReproduced(t *testing.T) {
	if _, err := Build("main", "2026-09-08T00:00:00Z"); err == nil {
		t.Fatal("a branch name was accepted as a version")
	}
	if _, err := Build("v0.1.0", "yesterday"); err == nil {
		t.Fatal("a non-RFC3339 date was accepted")
	}
}
