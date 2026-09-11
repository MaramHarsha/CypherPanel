//go:build linux || darwin

package guard

import (
	"fmt"
	"syscall"
)

// FreeBytes reports the bytes available to unprivileged writes on the
// filesystem containing path.
func FreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("guard: statfs %q: %w", path, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // Bavail/Bsize types differ per platform
}
