//go:build !linux

package rest

// diskUsage is unavailable off Linux. Zeros are read as "unknown" and never as
// "full", so a panel that cannot answer is silent rather than alarming
// (disk-management.md §6).
func diskUsage(string) (total, free uint64, ok bool) { return 0, 0, false }
