//go:build linux

package phpruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/MaramHarsha/CypherPanel/internal/paths"
)

// Run installs or uninstalls a PHP branch by executing the package-manager
// commands for the detected distro family, in order. Non-interactive; stops at
// the first failing command.
func Run(ctx context.Context, family paths.Family, version, action string) error {
	cmds, err := Commands(family, version, action)
	if err != nil {
		return err
	}
	for _, c := range cmds {
		cmd := exec.CommandContext(ctx, c.Name, c.Args...)
		// Never block on a package-manager prompt.
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("phpruntime: %s %v: %w: %s", c.Name, c.Args, err, out)
		}
	}
	return nil
}
