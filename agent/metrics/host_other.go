//go:build !linux

package metrics

// The host sampler is Linux-only: /proc/stat and /proc/meminfo are Linux
// interfaces, and ADR-006 puts the agent on Linux hosts. On anything else the
// node reports no host row, which reads downstream as "not reported" rather
// than as a measurement of zero.

type HostSample struct {
	CPUTotalJiffies uint64
	CPUIdleJiffies  uint64
	Cores           int
	MemoryTotal     uint64
	MemoryUsed      uint64
	DiskTotal       uint64
	DiskFree        uint64
}

func ReadHost(string) (HostSample, error) { return HostSample{}, nil }
