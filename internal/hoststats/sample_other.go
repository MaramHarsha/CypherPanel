//go:build !linux

package hoststats

// Non-Linux dev machines: no metrics, heartbeat still works.
func sample() Stats {
	return Stats{}
}
