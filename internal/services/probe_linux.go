//go:build linux

package services

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Control runs a lifecycle action (start|stop|restart|reload) on a managed
// service via systemd. Callers must validate service and action first
// (ValidAction / IsManaged); this only refuses to run without systemd.
func Control(ctx context.Context, service, action string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return ErrUnsupported
	}
	out, err := exec.CommandContext(ctx, "systemctl", action, service).CombinedOutput()
	if err != nil {
		return fmt.Errorf("services: systemctl %s %s: %w: %s", action, service, err, out)
	}
	return nil
}

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
