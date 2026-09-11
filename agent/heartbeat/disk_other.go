//go:build !linux

package heartbeat

// diskUsage is unavailable off Linux. The plane reads zeros as "unknown" and
// never as "full", so a host that cannot answer is silent rather than alarming
// (disk-management.md §4).
func diskUsage(string) (total, free uint64, ok bool) { return 0, 0, false }
