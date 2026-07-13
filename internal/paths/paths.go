// Package paths is CypherAgent's distro path-mapping layer. All system
// locations the agent touches are resolved here — per distro family, with
// environment overrides — so no filesystem path is ever hardcoded in feature
// code and the same binary serves Debian/Ubuntu and RHEL-family servers.
package paths

import (
	"os"
	"path/filepath"
)

// Family is a Linux distribution family with a shared filesystem layout.
type Family string

const (
	FamilyDebian  Family = "debian" // Debian, Ubuntu
	FamilyRHEL    Family = "rhel"   // RHEL, AlmaLinux, Rocky, CloudLinux
	FamilyUnknown Family = "unknown"
)

// Layout maps every system location CypherAgent manages. Fields are resolved
// from distro-family defaults, then overridden by CYPHER_PATH_* environment
// variables (and later by the agent config file).
type Layout struct {
	NginxConfDir   string // per-vhost config files
	NginxMainConf  string
	PHPFPMPoolDir  string // pattern: {PHPFPMPoolDir}/{phpversion}/pool.d
	SystemdUnitDir string
	HomeRoot       string // hosted-account home directories
	WebRootName    string // directory name inside an account home, e.g. "public_html"
	ACMEWebRoot    string // HTTP-01 challenge directory
	RunDir         string // sockets, pidfiles
}

// ForFamily returns the default layout for a distro family, with any
// CYPHER_PATH_* environment overrides applied.
func ForFamily(f Family) Layout {
	l := Layout{
		NginxMainConf:  "/etc/nginx/nginx.conf",
		SystemdUnitDir: "/etc/systemd/system",
		HomeRoot:       "/home",
		WebRootName:    "public_html",
		ACMEWebRoot:    "/var/lib/cypherpanel/acme",
		RunDir:         "/run/cypherpanel",
	}
	switch f {
	case FamilyDebian:
		l.NginxConfDir = "/etc/nginx/sites-enabled"
		l.PHPFPMPoolDir = "/etc/php"
	case FamilyRHEL:
		l.NginxConfDir = "/etc/nginx/conf.d"
		l.PHPFPMPoolDir = "/etc/opt/remi" // multi-PHP via Remi/SCL-style trees
	default:
		l.NginxConfDir = "/etc/nginx/conf.d"
		l.PHPFPMPoolDir = "/etc/php"
	}
	l.applyEnvOverrides()
	return l
}

// Detect resolves the running system's distro family and returns its layout.
// On non-Linux platforms (dev machines) it returns FamilyUnknown defaults.
func Detect() (Family, Layout) {
	f := detectFamily()
	return f, ForFamily(f)
}

// AccountHome returns the home directory for a hosted account username.
func (l Layout) AccountHome(username string) string {
	return filepath.Join(l.HomeRoot, username)
}

// AccountWebRoot returns the public web root for a hosted account.
func (l Layout) AccountWebRoot(username string) string {
	return filepath.Join(l.HomeRoot, username, l.WebRootName)
}

// VhostConfPath returns the web-server config file path for a domain.
func (l Layout) VhostConfPath(domain string) string {
	return filepath.Join(l.NginxConfDir, domain+".conf")
}

func (l *Layout) applyEnvOverrides() {
	override := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	override(&l.NginxConfDir, "CYPHER_PATH_NGINX_CONF_DIR")
	override(&l.NginxMainConf, "CYPHER_PATH_NGINX_MAIN_CONF")
	override(&l.PHPFPMPoolDir, "CYPHER_PATH_PHPFPM_POOL_DIR")
	override(&l.SystemdUnitDir, "CYPHER_PATH_SYSTEMD_UNIT_DIR")
	override(&l.HomeRoot, "CYPHER_PATH_HOME_ROOT")
	override(&l.WebRootName, "CYPHER_PATH_WEB_ROOT_NAME")
	override(&l.ACMEWebRoot, "CYPHER_PATH_ACME_WEB_ROOT")
	override(&l.RunDir, "CYPHER_PATH_RUN_DIR")
}
