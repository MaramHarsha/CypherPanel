// Package phpruntime installs and removes PHP-FPM branches on a managed server
// via the distro package manager. Command construction is pure and
// distro-aware (unit-testable on any OS); only execution is Linux-specific.
//
// The set of installable versions is an operator decision (CYPHER_PHP_VERSIONS,
// validated in Core); here we only guard the version string's shape before it
// is interpolated into a package name, and map it to the right package names
// for the distro family.
package phpruntime

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/MaramHarsha/CypherPanel/internal/paths"
)

// ErrUnsupported is returned when runtime management isn't available (non-Linux
// dev machines, or an unknown distro family with no known package layout).
var ErrUnsupported = errors.New("phpruntime: operation only supported on Linux servers")

// versionRe constrains a PHP version to <major>.<minor> so it can never inject
// extra tokens into a package name / shell argument vector.
var versionRe = regexp.MustCompile(`^\d+\.\d+$`)

var actions = map[string]bool{"install": true, "uninstall": true}

// ValidAction reports whether action is a permitted verb.
func ValidAction(action string) bool { return actions[action] }

// ValidVersion reports whether version is a well-formed <major>.<minor>.
func ValidVersion(version string) bool { return versionRe.MatchString(version) }

// Command is one package-manager invocation.
type Command struct {
	Name string
	Args []string
}

// extensions are the commonly-needed PHP modules installed alongside FPM so a
// freshly-installed branch can actually run typical apps.
var extensions = []string{"cli", "common", "mysql", "curl", "mbstring", "xml", "gd", "zip", "intl", "bcmath"}

// Commands returns the ordered package-manager commands to install or remove a
// PHP branch on the given distro family. Pure and deterministic.
func Commands(family paths.Family, version, action string) ([]Command, error) {
	if !ValidVersion(version) {
		return nil, fmt.Errorf("phpruntime: invalid version %q", version)
	}
	if !ValidAction(action) {
		return nil, fmt.Errorf("phpruntime: invalid action %q", action)
	}

	switch family {
	case paths.FamilyDebian:
		// Debian/Ubuntu (Sury/deb.sury.org layout): php8.3-fpm, php8.3-mysql, …
		pkgs := []string{"php" + version + "-fpm"}
		for _, e := range extensions {
			pkgs = append(pkgs, "php"+version+"-"+e)
		}
		if action == "install" {
			return []Command{
				{Name: "apt-get", Args: []string{"update"}},
				{Name: "apt-get", Args: append([]string{"install", "-y"}, pkgs...)},
			}, nil
		}
		return []Command{{Name: "apt-get", Args: []string{"remove", "-y", "php" + version + "-fpm"}}}, nil

	case paths.FamilyRHEL:
		// RHEL family (Remi): php83-php-fpm, php83-php-mysqlnd, … (version dotless).
		// The Remi repo + `dnf module reset php` are an install-time prerequisite.
		v := strings.ReplaceAll(version, ".", "")
		prefix := "php" + v + "-php-"
		pkgs := []string{prefix + "fpm"}
		for _, e := range extensions {
			pkgs = append(pkgs, prefix+e)
		}
		if action == "install" {
			return []Command{{Name: "dnf", Args: append([]string{"install", "-y"}, pkgs...)}}, nil
		}
		return []Command{{Name: "dnf", Args: []string{"remove", "-y", prefix + "fpm"}}}, nil

	default:
		return nil, ErrUnsupported
	}
}
