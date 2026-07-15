package usersdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// MariaDB is the MVP-default Manager. It runs DDL as the admin connection from
// the provided *sql.DB (a provisioning account with GRANT OPTION).
type MariaDB struct{ db *sql.DB }

// OpenMariaDB connects to the admin MariaDB using a go-sql-driver/mysql DSN,
// e.g. "root:pass@tcp(127.0.0.1:3306)/". The caller closes it.
func OpenMariaDB(dsn string) (*MariaDB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("usersdb: open: %w", err)
	}
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetMaxOpenConns(4)
	return &MariaDB{db: db}, nil
}

func NewMariaDB(db *sql.DB) *MariaDB { return &MariaDB{db: db} }

func (m *MariaDB) Close() error { return m.db.Close() }

// Provision creates the database + user + least-privilege grant. Idempotent:
// re-running converges and re-asserts the password (so a redelivered task and
// the returned credential always agree). Errors deliberately omit the SQL text
// because the CREATE/ALTER USER statements embed the password.
func (m *MariaDB) Provision(ctx context.Context, spec Spec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	if spec.Password == "" {
		return fmt.Errorf("usersdb: provision requires a password")
	}
	steps := []struct {
		what string
		sql  string
	}{
		{"create database", fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", spec.Database)},
		{"create user", fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY '%s'", spec.User, spec.Host, spec.Password)},
		{"set password", fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s'", spec.User, spec.Host, spec.Password)},
		{"grant", fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s'", spec.Database, spec.User, spec.Host)},
		{"flush", "FLUSH PRIVILEGES"},
	}
	for _, s := range steps {
		if _, err := m.db.ExecContext(ctx, s.sql); err != nil {
			// Never echo s.sql — steps 2/3 contain the password.
			return fmt.Errorf("usersdb: %s failed: %w", s.what, err)
		}
	}
	return nil
}

// Deprovision drops the database and its user. Idempotent (IF EXISTS).
func (m *MariaDB) Deprovision(ctx context.Context, spec Spec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	steps := []string{
		fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", spec.Database),
		fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s'", spec.User, spec.Host),
		"FLUSH PRIVILEGES",
	}
	for _, s := range steps {
		if _, err := m.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("usersdb: deprovision: %w", err)
		}
	}
	return nil
}
