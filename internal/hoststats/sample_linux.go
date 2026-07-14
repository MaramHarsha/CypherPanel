//go:build linux

package hoststats

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func sample() Stats {
	var s Stats
	s.Load1m = readLoad1m()
	s.MemoryTotalBytes, s.MemoryUsedBytes = readMemory()
	s.DiskTotalBytes, s.DiskUsedBytes = readRootDisk()
	return s
}

func readLoad1m() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	load, _ := strconv.ParseFloat(fields[0], 64)
	return load
}

func readMemory() (total, used uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	var totalKiB, availKiB uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			totalKiB = parseMeminfoKiB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			availKiB = parseMeminfoKiB(line)
		}
		if totalKiB > 0 && availKiB > 0 {
			break
		}
	}
	if totalKiB == 0 {
		return 0, 0
	}
	total = totalKiB * 1024
	if availKiB <= totalKiB {
		used = (totalKiB - availKiB) * 1024
	}
	return total, used
}

func parseMeminfoKiB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(fields[1], 10, 64)
	return v
}

func readRootDisk() (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	free := st.Bfree * bsize
	if free <= total {
		used = total - free
	}
	return total, used
}
