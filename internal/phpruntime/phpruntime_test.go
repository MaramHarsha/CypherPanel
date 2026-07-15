package phpruntime

import (
	"errors"
	"strings"
	"testing"

	"github.com/MaramHarsha/CypherPanel/internal/paths"
)

func TestCommands_DebianInstall(t *testing.T) {
	cmds, err := Commands(paths.FamilyDebian, "8.3", "install")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 {
		t.Fatalf("want 2 commands (update, install), got %d", len(cmds))
	}
	if cmds[0].Name != "apt-get" || cmds[0].Args[0] != "update" {
		t.Fatalf("first command = %+v, want apt-get update", cmds[0])
	}
	joined := cmds[1].Name + " " + strings.Join(cmds[1].Args, " ")
	if !strings.Contains(joined, "install -y php8.3-fpm") {
		t.Fatalf("install command missing php8.3-fpm: %q", joined)
	}
	if !strings.Contains(joined, "php8.3-mysql") {
		t.Fatalf("install command missing extensions: %q", joined)
	}
}

func TestCommands_DebianUninstall(t *testing.T) {
	cmds, err := Commands(paths.FamilyDebian, "8.3", "uninstall")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 {
		t.Fatalf("want 1 command, got %d", len(cmds))
	}
	joined := strings.Join(cmds[0].Args, " ")
	if joined != "remove -y php8.3-fpm" {
		t.Fatalf("uninstall args = %q", joined)
	}
}

func TestCommands_RHELInstallUsesRemiNaming(t *testing.T) {
	cmds, err := Commands(paths.FamilyRHEL, "8.3", "install")
	if err != nil {
		t.Fatal(err)
	}
	joined := cmds[0].Name + " " + strings.Join(cmds[0].Args, " ")
	if cmds[0].Name != "dnf" || !strings.Contains(joined, "php83-php-fpm") {
		t.Fatalf("RHEL install = %q, want dnf ... php83-php-fpm", joined)
	}
}

func TestCommands_RejectsInjectionAndBadInput(t *testing.T) {
	for _, bad := range []string{"8.3; rm -rf /", "8", "8.x", "$(id)", "8.3 8.4"} {
		if _, err := Commands(paths.FamilyDebian, bad, "install"); err == nil {
			t.Fatalf("version %q should be rejected", bad)
		}
	}
	if _, err := Commands(paths.FamilyDebian, "8.3", "purge"); err == nil {
		t.Fatal("action 'purge' should be rejected")
	}
	if _, err := Commands(paths.FamilyUnknown, "8.3", "install"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown family should be ErrUnsupported, got %v", err)
	}
}
