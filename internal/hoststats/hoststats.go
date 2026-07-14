// Package hoststats samples basic host metrics (load, memory, root-disk
// usage) for agent heartbeats. This is liveness telemetry only — historical
// metrics belong in a time-series store, not here (plan.md Section 8).
package hoststats

// Stats is a point-in-time host snapshot. Zero values mean "unavailable"
// (non-Linux dev machines report all zeros).
type Stats struct {
	Load1m           float64
	MemoryTotalBytes uint64
	MemoryUsedBytes  uint64
	DiskTotalBytes   uint64
	DiskUsedBytes    uint64
}

// Sample collects current host stats. It never fails: metrics that cannot be
// read are left at zero, because a heartbeat must not be blocked by a
// telemetry hiccup.
func Sample() Stats {
	return sample()
}
