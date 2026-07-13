// Package platform isolates everything that only exists on Linux servers
// (system users, systemd, cgroups) behind interfaces. Feature code depends on
// these interfaces and stays compilable and unit-testable on any OS; the real
// implementations live in _linux.go files (plan.md: Portability).
package platform

import (
	"context"
	"errors"
)

// ErrUnsupported is returned by the stub implementation on non-Linux
// platforms (development machines).
var ErrUnsupported = errors.New("platform: operation only supported on Linux servers")

// SystemUsers manages the dedicated Linux user each hosted account runs as
// (plan.md Section 7: Dedicated System Users).
type SystemUsers interface {
	// Create adds a locked (no-password) system user with the given home
	// directory. Idempotent: creating an existing user is not an error.
	Create(ctx context.Context, username, homeDir string) error
	// Remove deletes a system user. Idempotent.
	Remove(ctx context.Context, username string) error
	// Exists reports whether the user is present.
	Exists(ctx context.Context, username string) (bool, error)
}

// New returns the platform implementation for the current OS.
func New() SystemUsers {
	return newSystemUsers()
}
