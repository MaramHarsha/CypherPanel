// Package usersdb provisions hosted-account *user* databases (plan.md §4B).
// MariaDB is the MVP default behind the Manager interface, so a PostgreSQL
// user-DB adapter can drop in later without touching the task/API layer. This
// is entirely separate from CypherCore's own control-plane Postgres.
package usersdb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
)

// ErrUnsupported is returned when no user-DB backend is configured (e.g. the
// agent has no MariaDB DSN), so the task fails permanently rather than retrying.
var ErrUnsupported = errors.New("usersdb: no user-database backend configured")

// identRe constrains database/user identifiers so they are safe to interpolate
// into DDL (identifiers cannot be bound as parameters). Core namespaces and
// validates too; this is the defense-in-depth check at the point of use.
var identRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// hostRe constrains the connect-host grant scope (e.g. localhost, 10.0.0.%).
var hostRe = regexp.MustCompile(`^[a-zA-Z0-9_.%-]{1,255}$`)

// Spec is one account database and its owning user.
type Spec struct {
	Database string
	User     string
	Host     string
	Password string // set for Provision; ignored for Deprovision
}

func (s Spec) validate() error {
	if !identRe.MatchString(s.Database) {
		return fmt.Errorf("usersdb: invalid database name %q", s.Database)
	}
	if !identRe.MatchString(s.User) {
		return fmt.Errorf("usersdb: invalid db user %q", s.User)
	}
	if !hostRe.MatchString(s.Host) {
		return fmt.Errorf("usersdb: invalid db host %q", s.Host)
	}
	return nil
}

// Manager provisions and removes account databases. Implementations must be
// idempotent (re-running converges — create-if-not-exists, drop-if-exists).
type Manager interface {
	Provision(ctx context.Context, spec Spec) error
	Deprovision(ctx context.Context, spec Spec) error
}

// GeneratePassword returns a strong random password safe to embed in a SQL
// string literal (URL-safe base64 has no quotes or backslashes).
func GeneratePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("usersdb: generating password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
