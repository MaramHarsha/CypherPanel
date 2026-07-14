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

// SiteSpec is the fully-resolved on-disk desired state for one site: which
// directories to own, and which already-rendered config files to place.
type SiteSpec struct {
	Username    string   // account system user (owner of account-tree dirs)
	AccountDirs []string // dirs to create owned by the account user (web root, logs)
	VHostPath   string   // web-server vhost config destination (root-owned)
	VHostConfig []byte
	PoolPath    string // php-fpm pool config destination (root-owned)
	PoolConfig  []byte
}

// Sites applies rendered web/PHP configs to the host. Applying (mkdir+chown,
// write, validate, reload) is OS-specific; rendering lives in internal/webserver.
type Sites interface {
	// Provision creates account-owned directories, writes the configs, then
	// validates and reloads the web server. Idempotent: re-applying the same
	// desired state converges. On validation failure the offending config is
	// rolled back and an error is returned (never leave a broken vhost).
	Provision(ctx context.Context, spec SiteSpec) error
	// Deprovision removes a site's configs and reloads. Idempotent.
	Deprovision(ctx context.Context, vhostPath, poolPath string) error
}

// NewSites returns the platform Sites implementation for the current OS.
func NewSites() Sites {
	return newSites()
}
