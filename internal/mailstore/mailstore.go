// Package mailstore manages the virtual-mailbox auth database that Postfix and
// Dovecot query (address → password hash, maildir, quota). MariaDB is the MVP
// backend behind the Manager interface. Mail users are virtual — decoupled from
// Linux logins (mail-stack skill). This DB lives on the account's server; the
// agent writes it, and the MTA reads it via its own SQL maps.
package mailstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// ErrUnsupported is returned when no mail backend is configured on the server.
var ErrUnsupported = errors.New("mailstore: no mail backend configured")

var addressRe = regexp.MustCompile(`^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$`)

// Mailbox is one virtual mailbox to provision.
type Mailbox struct {
	Address      string
	Domain       string
	Maildir      string
	PasswordHash string
	QuotaBytes   int64
}

func (m Mailbox) validate() error {
	if !addressRe.MatchString(m.Address) {
		return fmt.Errorf("mailstore: invalid address %q", m.Address)
	}
	if m.PasswordHash == "" {
		return errors.New("mailstore: password hash required")
	}
	return nil
}

// Manager provisions virtual mailboxes. Idempotent (upsert / delete converge).
type Manager interface {
	EnsureSchema(ctx context.Context) error
	UpsertMailbox(ctx context.Context, m Mailbox) error
	DeleteMailbox(ctx context.Context, address string) error
}

// MariaDB is the MVP-default Manager.
type MariaDB struct{ db *sql.DB }

// OpenMariaDB connects to the mail auth database.
func OpenMariaDB(dsn string) (*MariaDB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("mailstore: open: %w", err)
	}
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetMaxOpenConns(4)
	return &MariaDB{db: db}, nil
}

func (m *MariaDB) Close() error { return m.db.Close() }

// EnsureSchema creates the virtual_domains / virtual_users tables Postfix and
// Dovecot's SQL maps expect. Idempotent.
func (m *MariaDB) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS virtual_domains (
			id INT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL UNIQUE
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS virtual_users (
			id INT AUTO_INCREMENT PRIMARY KEY,
			domain_id INT NOT NULL,
			email VARCHAR(255) NOT NULL UNIQUE,
			password VARCHAR(255) NOT NULL,
			maildir VARCHAR(255) NOT NULL,
			quota BIGINT NOT NULL DEFAULT 0,
			CONSTRAINT fk_vu_domain FOREIGN KEY (domain_id)
				REFERENCES virtual_domains(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
	}
	for _, s := range stmts {
		if _, err := m.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("mailstore: schema: %w", err)
		}
	}
	return nil
}

// UpsertMailbox ensures the domain exists and inserts/updates the mailbox row.
// Errors omit the SQL (which embeds the password hash).
func (m *MariaDB) UpsertMailbox(ctx context.Context, mb Mailbox) error {
	if err := mb.validate(); err != nil {
		return err
	}
	if _, err := m.db.ExecContext(ctx, `INSERT IGNORE INTO virtual_domains (name) VALUES (?)`, mb.Domain); err != nil {
		return fmt.Errorf("mailstore: ensure domain: %w", err)
	}
	var domainID int
	if err := m.db.QueryRowContext(ctx, `SELECT id FROM virtual_domains WHERE name = ?`, mb.Domain).Scan(&domainID); err != nil {
		return fmt.Errorf("mailstore: domain lookup: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO virtual_users (domain_id, email, password, maildir, quota)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE password = VALUES(password), maildir = VALUES(maildir), quota = VALUES(quota)`,
		domainID, mb.Address, mb.PasswordHash, mb.Maildir, mb.QuotaBytes); err != nil {
		return errors.New("mailstore: upserting mailbox failed")
	}
	return nil
}

// DeleteMailbox removes a mailbox row (idempotent).
func (m *MariaDB) DeleteMailbox(ctx context.Context, address string) error {
	if _, err := m.db.ExecContext(ctx, `DELETE FROM virtual_users WHERE email = ?`, address); err != nil {
		return fmt.Errorf("mailstore: delete mailbox: %w", err)
	}
	return nil
}
