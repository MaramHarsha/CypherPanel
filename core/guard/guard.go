// Package guard is the control plane's boot-time self-protection
// (threat-model §5.9, §8 requirement 10): silent disk exhaustion is the #1
// production killer for both reference products, so cypherd refuses to start
// into a nearly-full disk instead of limping along until its own Postgres can
// no longer write.
package guard

import (
	"errors"
	"fmt"
)

// ErrUnsupported is returned by FreeBytes on platforms without filesystem
// statistics; callers may degrade to a warning (dev builds on such platforms
// are fine — production targets are linux, tech-stack.md).
var ErrUnsupported = errors.New("guard: disk statistics unsupported on this platform")

// StatFunc reports the bytes available to unprivileged writes on the
// filesystem containing path. Injected so the threshold logic is testable
// (ENGINEERING rule 9); production callers pass FreeBytes.
type StatFunc func(path string) (uint64, error)

// CheckDiskHeadroom returns an error when the filesystem containing path has
// fewer than minFree bytes available. minFree 0 disables the check (an
// explicit operator choice via config, never a silent default).
func CheckDiskHeadroom(path string, minFree uint64, stat StatFunc) error {
	if minFree == 0 {
		return nil
	}
	free, err := stat(path)
	if err != nil {
		return fmt.Errorf("guard: checking disk headroom at %q: %w", path, err)
	}
	if free < minFree {
		return fmt.Errorf(
			"guard: only %d bytes free at %q, below the %d-byte minimum — free disk space or lower CYPHERD_MIN_DISK_FREE (threat-model §5.9)",
			free, path, minFree,
		)
	}
	return nil
}
