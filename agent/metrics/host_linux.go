//go:build linux

package metrics

// The host's OWN CPU, memory and data-root filesystem, as one
// resource_kind = "server" row per bucket (metrics-and-usage §4.1, and the row
// threshold-alerts.md §3.2 needs).
//
// Why this exists at all: every other sample here is attributed to a MANAGED
// container, so a server figure summed from those is the share of the host
// taken by CypherPanel's own workloads. Alerting on that is wrong in the
// dangerous direction — on a box the operator's own processes filled, it
// reports calm. So the host reads its own totals, from the kernel, and the two
// numbers stay distinguishable rather than one standing in for the other.

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// HostSample is the node itself.
type HostSample struct {
	// CPUTotalJiffies and CPUIdleJiffies are cumulative, so the collector
	// differences them the same way it differences a container's counter — a
	// slow read shifts nothing.
	CPUTotalJiffies uint64
	CPUIdleJiffies  uint64
	Cores           int
	MemoryTotal     uint64
	MemoryUsed      uint64
	DiskTotal       uint64
	DiskFree        uint64
}

// ReadHost samples /proc/stat, /proc/meminfo and statfs on dataRoot. A field it
// cannot read stays zero, and zero is read as "not reported" everywhere
// downstream rather than as a measurement of nothing.
func ReadHost(dataRoot string) (HostSample, error) {
	var out HostSample

	if f, err := os.Open("/proc/stat"); err == nil {
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "cpu ") {
				fields := strings.Fields(line)[1:]
				for i, raw := range fields {
					n, err := strconv.ParseUint(raw, 10, 64)
					if err != nil {
						continue
					}
					out.CPUTotalJiffies += n
					// Fields 3 and 4 are idle and iowait. A host waiting on
					// disk is not a host doing work, and counting iowait as
					// busy is how a slow disk reads as a CPU problem.
					if i == 3 || i == 4 {
						out.CPUIdleJiffies += n
					}
				}
				continue
			}
			if strings.HasPrefix(line, "cpu") {
				out.Cores++
			}
		}
	}

	if f, err := os.Open("/proc/meminfo"); err == nil {
		defer func() { _ = f.Close() }()
		var total, available uint64
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			n, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				total = n * 1024
			case "MemAvailable:":
				// MemAvailable, not MemFree: the page cache is reclaimable, and
				// a host with a warm cache is not a host out of memory.
				available = n * 1024
			}
		}
		out.MemoryTotal = total
		if total > available {
			out.MemoryUsed = total - available
		}
	}

	if dataRoot != "" {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dataRoot, &st); err == nil {
			out.DiskTotal = st.Blocks * uint64(st.Bsize)
			out.DiskFree = st.Bavail * uint64(st.Bsize)
		}
	}
	return out, nil
}
