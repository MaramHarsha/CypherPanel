//go:build !linux

package services

import "context"

// Non-Linux dev machines have no systemd; monitoring is unavailable.
func probe(names []string) []Status {
	return nil
}

// Control is unsupported off Linux.
func Control(_ context.Context, _, _ string) error {
	return ErrUnsupported
}
