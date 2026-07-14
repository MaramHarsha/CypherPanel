//go:build linux

package services

import (
	"context"
	"os/exec"
	"time"
)

// probe queries systemd for each managed unit. If systemctl is unavailable
// (no systemd), it returns nil — monitoring is simply unavailable, never an
// error that could disrupt a heartbeat.
func probe(names []string) []Status {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out []Status
	for _, name := range names {
		cmd := exec.CommandContext(ctx, "systemctl", "show", "-p", "LoadState", "-p", "ActiveState", name)
		raw, err := cmd.Output()
		if err != nil {
			// Could not query this unit; report unknown rather than dropping it.
			out = append(out, Status{Name: name, State: "unknown"})
			continue
		}
		state, installed := parseShow(string(raw))
		if !installed {
			continue // not installed on this server — omit
		}
		out = append(out, Status{Name: name, State: state})
	}
	return out
}
