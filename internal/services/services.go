// Package services samples the state of managed system services (nginx,
// mariadb, ...) for agent heartbeats, via systemd. Like hoststats, it is
// telemetry: a probe failure degrades to "unknown"/omitted, it never fails a
// heartbeat. Linux-only real implementation lives in probe_linux.go.
package services

import (
	"errors"
	"os"
	"strings"
)

// ErrUnsupported is returned by Control on non-Linux/dev platforms.
var ErrUnsupported = errors.New("services: control only supported on Linux servers with systemd")

// controlActions are the lifecycle verbs a service may be driven through. Kept
// deliberately small — no arbitrary systemctl subcommands.
var controlActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "reload": true,
}

// ValidAction reports whether action is a permitted lifecycle verb.
func ValidAction(action string) bool { return controlActions[action] }

// IsManaged reports whether name is in the managed-services allowlist. Control
// refuses anything else so a task payload can never target an arbitrary unit.
func IsManaged(name string) bool {
	for _, s := range ManagedServices() {
		if s == name {
			return true
		}
	}
	return false
}

// Status is a managed service's current state.
type Status struct {
	Name  string
	State string // active | inactive | failed | activating | unknown
}

// defaultManaged is the MVP-default service set (plan.md §4A). Names are
// consistent across Debian and RHEL families for these; operators override
// via CYPHER_MANAGED_SERVICES when their unit names differ.
var defaultManaged = []string{"nginx", "mariadb", "postfix", "dovecot", "pdns"}

// ManagedServices returns the systemd unit names to monitor.
func ManagedServices() []string {
	if v := os.Getenv("CYPHER_MANAGED_SERVICES"); v != "" {
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return defaultManaged
}

// Sample returns the state of every managed, installed service. Not-installed
// units are omitted (a server without a mail stack shouldn't report Dovecot).
func Sample() []Status {
	return probe(ManagedServices())
}

// parseShow interprets `systemctl show -p LoadState -p ActiveState` output for
// one unit. installed is false when the unit is not present (LoadState=
// not-found), in which case it should be omitted from the report.
func parseShow(out string) (state string, installed bool) {
	state = "unknown"
	installed = true
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			if val == "not-found" {
				return "", false
			}
		case "ActiveState":
			if val != "" {
				state = val
			}
		}
	}
	return state, installed
}
