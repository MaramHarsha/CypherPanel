package rest

import (
	"os"
	"strings"
	"testing"
)

// install.sh writes cypherd.service from a heredoc — it is a curl|sh single
// file and cannot read the repository — while docs/dev/deployment.md's manual
// path copies install/cypherd.service. Two copies of a sandboxed unit drift,
// and they had: one waited on docker.service and the other did not, and only
// one of them could have carried the upgrade handoff. This holds them together.
func TestInstallScriptAndUnitFileAgree(t *testing.T) {
	script, err := os.ReadFile("../../../install/install.sh")
	if err != nil {
		t.Skip("install/install.sh not beside this package")
	}
	unit, err := os.ReadFile("../../../install/cypherd.service")
	if err != nil {
		t.Skip("install/cypherd.service not beside this package")
	}
	const open = "cat > \"$UNIT\" <<'EOF'\n"
	body := string(script)
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatal("install.sh no longer writes $UNIT from a heredoc")
	}
	body = body[i+len(open):]
	j := strings.Index(body, "\nEOF\n")
	if j < 0 {
		t.Fatal("the heredoc never closes")
	}
	fromScript := strings.TrimSpace(body[:j])

	// The checked-in file carries a comment header the script does not; the
	// unit itself starts at [Unit].
	fromFile := string(unit)
	k := strings.Index(fromFile, "[Unit]")
	if k < 0 {
		t.Fatal("install/cypherd.service has no [Unit] section")
	}
	fromFile = strings.TrimSpace(fromFile[k:])

	if fromScript != fromFile {
		t.Fatalf("install/cypherd.service and install.sh's heredoc differ.\n--- install.sh ---\n%s\n--- cypherd.service ---\n%s", fromScript, fromFile)
	}
	for _, want := range []string{"SupplementaryGroups=cypherpanel-upgrade", "ReadWritePaths=-/var/lib/cypherpanel/upgrade", "DynamicUser=true"} {
		if !strings.Contains(fromFile, want) {
			t.Errorf("the unit lost %q", want)
		}
	}
}
