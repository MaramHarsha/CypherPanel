//go:build !linux

package phpruntime

import (
	"context"

	"github.com/MaramHarsha/CypherPanel/internal/paths"
)

// Run is unsupported off Linux (dev machines have no distro package manager).
func Run(_ context.Context, _ paths.Family, _, _ string) error {
	return ErrUnsupported
}
