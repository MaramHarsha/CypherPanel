package paths

import (
	"path/filepath"
	"testing"
)

func TestFamilyLayoutsDiffer(t *testing.T) {
	deb := ForFamily(FamilyDebian)
	rhel := ForFamily(FamilyRHEL)
	if deb.NginxConfDir == rhel.NginxConfDir {
		t.Fatal("Debian and RHEL nginx conf dirs must differ — that's the point of the layer")
	}
}

func TestEnvOverrideWins(t *testing.T) {
	t.Setenv("CYPHER_PATH_HOME_ROOT", "/srv/hosting")
	l := ForFamily(FamilyDebian)
	if l.HomeRoot != "/srv/hosting" {
		t.Fatalf("env override ignored: got %q", l.HomeRoot)
	}
	want := filepath.Join("/srv/hosting", "alice", "public_html")
	if got := l.AccountWebRoot("alice"); got != want {
		t.Fatalf("AccountWebRoot = %q, want %q", got, want)
	}
}

func TestVhostConfPath(t *testing.T) {
	l := ForFamily(FamilyDebian)
	want := filepath.Join(l.NginxConfDir, "example.com.conf")
	if got := l.VhostConfPath("example.com"); got != want {
		t.Fatalf("VhostConfPath = %q, want %q", got, want)
	}
}
