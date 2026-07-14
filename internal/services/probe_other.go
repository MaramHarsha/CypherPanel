//go:build !linux

package services

// Non-Linux dev machines have no systemd; monitoring is unavailable.
func probe(names []string) []Status {
	return nil
}
