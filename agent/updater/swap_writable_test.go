package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A RUNNING binary must count as replaceable. The first writable() opened the
// binary itself for writing, which Linux refuses for any executing file
// (ETXTBSY) — so on every real host the updater excluded itself with "managed
// outside the panel", and the unit test never noticed because it checked a file
// nothing was running. This runs one.
func TestARunningBinaryIsStillReplaceable(t *testing.T) {
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep(1) on this host")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "cypher-agent")
	if err := copyFile(src, bin); err != nil {
		t.Fatalf("copying sleep: %v", err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the copy: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	time.Sleep(50 * time.Millisecond)

	// The old check, as a control: this is what every host answered.
	if f, err := os.OpenFile(bin, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = f.Close()
		t.Log("this kernel allows writing a running executable; the regression cannot be demonstrated here, only the fix")
	}
	if !writable(bin) {
		t.Fatal("a running binary in a writable directory was reported as not replaceable")
	}
	// And a directory that cannot be written to is the genuinely excluded case.
	if writable(filepath.Join(dir, "no-such-dir", "cypher-agent")) {
		t.Fatal("a binary in a directory that does not exist was reported as replaceable")
	}
}

// The release workflow writes the manifest with `sha256sum ./*`, so names carry
// a "./" the agent must not trip over.
func TestManifestNamesLoseTheirDotSlash(t *testing.T) {
	m, err := parseManifest([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  ./cypher-agent-linux-amd64\n"))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if _, ok := m["cypher-agent-linux-amd64"]; !ok {
		t.Fatalf("the ./ prefix was kept: %v", m)
	}
}
