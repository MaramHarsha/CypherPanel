package usersdb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Dumper produces a restorable logical dump of one account database. Backups
// coordinate dumps with the file snapshot: a file backup taken while a database
// is mid-write is not restorable, so the dump is written first and snapshotted
// alongside the account's files (see internal/backups).
type Dumper interface {
	Dump(ctx context.Context, database, destPath string) error
}

// dumpBinary resolves the dump tool from the environment rather than
// hardcoding it — MariaDB ships `mariadb-dump`, MySQL ships `mysqldump`, and
// distros disagree about which is present.
func dumpBinary() string {
	if v := os.Getenv("CYPHER_MYSQLDUMP_BIN"); v != "" {
		return v
	}
	for _, c := range []string{"mariadb-dump", "mysqldump"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "mysqldump"
}

// Dump writes a single-transaction logical dump of database to destPath.
//
// Credentials are passed via a 0600 --defaults-extra-file, never as argv:
// command-line passwords are visible to every user on the box through the
// process table.
func (m *MariaDB) Dump(ctx context.Context, database, destPath string) error {
	if !identRe.MatchString(database) {
		return fmt.Errorf("usersdb: invalid database name %q", database)
	}
	if m.dsn == "" {
		return fmt.Errorf("usersdb: dump requires an admin DSN")
	}
	cfg, err := mysql.ParseDSN(m.dsn)
	if err != nil {
		return fmt.Errorf("usersdb: parsing admin dsn: %w", err)
	}

	host, port := "localhost", "3306"
	if cfg.Net == "tcp" && cfg.Addr != "" {
		if h, p, serr := splitHostPort(cfg.Addr); serr == nil {
			host, port = h, p
		}
	}

	// MySQL option files use bare, unquoted values; a password containing '#'
	// or a newline would corrupt the file or inject an option, so reject those
	// rather than emit something the tool would silently misparse.
	if strings.ContainsAny(cfg.Passwd, "\n\r#") {
		return fmt.Errorf("usersdb: admin password contains characters unsupported in an option file")
	}

	optFile, err := os.CreateTemp("", "cypher-dump-*.cnf")
	if err != nil {
		return fmt.Errorf("usersdb: creating option file: %w", err)
	}
	defer os.Remove(optFile.Name())
	if err := optFile.Chmod(0o600); err != nil {
		optFile.Close()
		return fmt.Errorf("usersdb: securing option file: %w", err)
	}
	opts := fmt.Sprintf("[client]\nuser=%s\npassword=%s\nhost=%s\nport=%s\n",
		cfg.User, cfg.Passwd, host, port)
	if _, err := optFile.WriteString(opts); err != nil {
		optFile.Close()
		return fmt.Errorf("usersdb: writing option file: %w", err)
	}
	if err := optFile.Close(); err != nil {
		return fmt.Errorf("usersdb: closing option file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return fmt.Errorf("usersdb: preparing dump dir: %w", err)
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("usersdb: creating dump file: %w", err)
	}
	defer out.Close()

	// --single-transaction gives a consistent snapshot of InnoDB tables without
	// locking the account's site out of its own database for the dump's
	// duration. --routines/--triggers/--events keep the dump actually complete.
	cmd := exec.CommandContext(ctx, dumpBinary(),
		"--defaults-extra-file="+optFile.Name(),
		"--single-transaction", "--quick",
		"--routines", "--triggers", "--events",
		"--databases", database,
	)
	var stderr strings.Builder
	cmd.Stdout = out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("usersdb: dumping %s: %s", database, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// splitHostPort splits "host:port", tolerating a bare host.
func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "3306", nil
	}
	host, port := addr[:i], addr[i+1:]
	if host == "" || port == "" {
		return "", "", fmt.Errorf("usersdb: malformed address %q", addr)
	}
	return host, port, nil
}
