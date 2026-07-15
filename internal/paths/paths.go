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
	WebServerUser  string // system user the web server runs as (socket group owner)
	SSLDir         string // issued certificates + ACME account keys
	MailRoot       string // virtual-mailbox Maildir storage root
	DKIMDir        string // per-domain DKIM signing keys
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
		SSLDir:         "/var/lib/cypherpanel/ssl",
		MailRoot:       "/var/mail/vhosts",
		DKIMDir:        "/var/lib/cypherpanel/dkim",
	}
	switch f {
	case FamilyDebian:
		l.NginxConfDir = "/etc/nginx/sites-enabled"
		l.PHPFPMPoolDir = "/etc/php"
		l.WebServerUser = "www-data"
	case FamilyRHEL:
		l.NginxConfDir = "/etc/nginx/conf.d"
		l.PHPFPMPoolDir = "/etc/opt/remi" // multi-PHP via Remi/SCL-style trees
		l.WebServerUser = "nginx"
	default:
		l.NginxConfDir = "/etc/nginx/conf.d"
		l.PHPFPMPoolDir = "/etc/php"
		l.WebServerUser = "www-data"
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

// AccountLogDir returns the per-account web log directory.
func (l Layout) AccountLogDir(username string) string {
	return filepath.Join(l.HomeRoot, username, "logs")
}

// PHPFPMSocketPath returns the dedicated PHP-FPM socket for an account
// (plan.md §7 — one socket per user for isolation).
func (l Layout) PHPFPMSocketPath(username string) string {
	return filepath.Join(l.RunDir, "php-"+username+".sock")
}

// PHPFPMPoolPath returns the pool config file for an account on a given PHP
// version (Debian multi-PHP layout: /etc/php/<version>/fpm/pool.d/<user>.conf).
func (l Layout) PHPFPMPoolPath(phpVersion, username string) string {
	return filepath.Join(l.PHPFPMPoolDir, phpVersion, "fpm", "pool.d", username+".conf")
}

// SSLCertPath / SSLKeyPath are the issued fullchain + private key for a domain.
func (l Layout) SSLCertPath(domain string) string {
	return filepath.Join(l.SSLDir, domain, "fullchain.pem")
}

func (l Layout) SSLKeyPath(domain string) string {
	return filepath.Join(l.SSLDir, domain, "privkey.pem")
}

// ACMEAccountDir holds the persistent ACME account key (reused across issues
// so we don't register a new account each time).
func (l Layout) ACMEAccountDir() string {
	return filepath.Join(l.SSLDir, "accounts")
}

func (l *Layout) applyEnvOverrides() {
	override := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	override(&l.WebServerUser, "CYPHER_PATH_WEB_SERVER_USER")
	override(&l.NginxConfDir, "CYPHER_PATH_NGINX_CONF_DIR")
	override(&l.NginxMainConf, "CYPHER_PATH_NGINX_MAIN_CONF")
	override(&l.PHPFPMPoolDir, "CYPHER_PATH_PHPFPM_POOL_DIR")
	override(&l.SystemdUnitDir, "CYPHER_PATH_SYSTEMD_UNIT_DIR")
	override(&l.HomeRoot, "CYPHER_PATH_HOME_ROOT")
	override(&l.WebRootName, "CYPHER_PATH_WEB_ROOT_NAME")
	override(&l.ACMEWebRoot, "CYPHER_PATH_ACME_WEB_ROOT")
	override(&l.RunDir, "CYPHER_PATH_RUN_DIR")
	override(&l.SSLDir, "CYPHER_PATH_SSL_DIR")
	override(&l.MailRoot, "CYPHER_PATH_MAIL_ROOT")
	override(&l.DKIMDir, "CYPHER_PATH_DKIM_DIR")
}

// MaildirPath returns the absolute Maildir for a mailbox (rel is domain/user/).
func (l Layout) MaildirPath(rel string) string {
	return filepath.Join(l.MailRoot, rel)
}
