//go:build linux

package paths

import (
	"bufio"
	"os"
	"strings"
)

// detectFamily classifies the running distro via /etc/os-release (ID and
// ID_LIKE), the systemd-standard mechanism present on every supported distro.
func detectFamily() Family {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return FamilyUnknown
	}
	defer f.Close()

	ids := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "ID=") || strings.HasPrefix(line, "ID_LIKE=") {
			_, v, _ := strings.Cut(line, "=")
			ids += " " + strings.Trim(v, `"`)
		}
	}

	switch {
	case containsAny(ids, "debian", "ubuntu"):
		return FamilyDebian
	case containsAny(ids, "rhel", "fedora", "centos", "almalinux", "rocky", "cloudlinux"):
		return FamilyRHEL
	default:
		return FamilyUnknown
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
