//go:build linux

package heartbeat

import "syscall"

// diskUsage reports a filesystem's total and free bytes (disk-management.md §4).
//
// Free rather than available: this is what an operator sees in `df`, and the
// difference (root's reserved blocks) is not space a workload can use.
func diskUsage(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	//nolint:unconvert,gosec // Bsize is int64 on amd64 and int32 on some arches
	bsize := uint64(st.Bsize)
	if bsize == 0 {
		return 0, 0, false
	}
	return st.Blocks * bsize, st.Bavail * bsize, true
}
