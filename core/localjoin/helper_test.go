package localjoin

import (
	"errors"
	"strings"
	"testing"
)

// The dialog shows the installer's REASON, not the tail of its advice.
//
// agent.sh's fail() prints "error: <reason>" where the reason runs to several
// lines of remedies. Taking the last line showed an operator whose host had no
// release to download from a go build command, and nothing about the download.
func TestLastLineIsTheErrorNotTheAdvice(t *testing.T) {
	out := "=> downloading cypher-agent (amd64) from https://example.invalid/agent\n" +
		"\x1b[31merror:\x1b[0m could not download the agent binary from https://example.invalid/agent\n" +
		"\n" +
		"  The plane deliberately does not host binaries (ADR-010), so the installer\n" +
		"  needs one of these:\n" +
		"    * a reachable release asset\n" +
		"  Building from a source checkout: cd agent && go build -o /usr/local/bin/cypher-agent ./cmd/cypher-agent\n"
	got := lastLine(out, errors.New("exit status 1"))
	if !strings.HasPrefix(got, "error: could not download the agent binary") {
		t.Fatalf("lastLine = %q, want the error: line", got)
	}
	// With no error: line the last non-empty line is still the best guess, and
	// with no output at all the exit status is all there is.
	if got := lastLine("first\nsecond\n\n", errors.New("exit status 1")); got != "second" {
		t.Fatalf("fallback = %q, want second", got)
	}
	if got := lastLine("", errors.New("exit status 1")); got != "exit status 1" {
		t.Fatalf("empty = %q, want the exit status", got)
	}
}
