// release-manifest writes release.json — the machine-readable description of
// one release that the panel's own upgrade verifies before it downloads a byte
// (panel-updates.md §4, core/upgrade.VerifyRelease).
//
// It is a program rather than a heredoc in the workflow for one reason: CI
// writes this file and scripts/release-sign.sh REBUILDS it from the same tag
// and compares byte for byte before signing, exactly as it does the binaries.
// Two shell snippets drift; one program run twice does not. Nothing here is a
// release-time input except the tag and the commit date both sides already
// derive the same way — the compatibility floors are constants in
// core/upgrade, bumped in the commit that breaks the thing they guard.
//
//	release-manifest -version v0.1.0 -published-at 2026-09-08T00:00:00Z -out dist/release.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/core/upgrade"
)

const repo = "MaramHarsha/CypherPanel"

func main() {
	version := flag.String("version", "", "the release tag, e.g. v0.1.0")
	publishedAt := flag.String("published-at", "", "the tagged commit's date, RFC 3339 UTC — never the wall clock, which nothing can reproduce")
	out := flag.String("out", "", "where to write release.json (stdout when empty)")
	flag.Parse()

	body, err := Build(*version, *publishedAt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release-manifest:", err)
		os.Exit(2)
	}
	if *out == "" {
		_, _ = os.Stdout.Write(body)
		return
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil { //nolint:gosec // a release asset is public
		fmt.Fprintln(os.Stderr, "release-manifest:", err)
		os.Exit(1)
	}
}

// Build renders the manifest. Deterministic for a given tag and date, which is
// the property the signer's comparison depends on.
func Build(version, publishedAt string) ([]byte, error) {
	if !upgrade.ValidTag(version) {
		return nil, fmt.Errorf("%q is not a release tag (vMAJOR.MINOR.PATCH[-pre])", version)
	}
	if _, err := time.Parse(time.RFC3339, publishedAt); err != nil {
		return nil, fmt.Errorf("-published-at must be RFC 3339: %w", err)
	}
	m := upgrade.Manifest{
		Version:         version,
		SchemaVersion:   store.LatestMigration(),
		RollbackFloor:   upgrade.RollbackFloor,
		AgentMinVersion: upgrade.AgentMinVersion,
		NotesURL:        "https://github.com/" + repo + "/releases/tag/" + version,
		PublishedAt:     publishedAt,
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}
