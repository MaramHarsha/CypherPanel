//go:build linux

package rest

import "syscall"

// diskUsage reports a filesystem's total and free bytes (disk-management.md §6).
//
// Free rather than available, for the same reason the agent's copy uses it:
// this is what an operator sees in `df`, and root's reserved blocks are not
// space anything can write to.
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
