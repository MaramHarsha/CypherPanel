// Package ftp provisions per-account FTP virtual users. Pure-FTPd (via pure-pw)
// is the MVP default behind the Manager interface, so a ProFTPD adapter can
// drop in later without touching the task/API layer. Each virtual user maps to
// the hosting account's system uid/gid + home, so uploads are owned by the
// account user (isolation — see the filesystem-operations-safety skill).
package ftp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"path" // Unix path semantics — these are always remote Linux paths, never host paths
	"regexp"
)

// ErrUnsupported is returned when no FTP backend is available (non-Linux dev
// machines, or pure-pw absent), so the task fails permanently rather than retrying.
var ErrUnsupported = errors.New("ftp: no FTP backend configured")

var loginRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Spec is one FTP virtual user.
type Spec struct {
	Username   string
	SystemUser string
	HomeDir    string
	Password   string // set for Provision
}

func (s Spec) validate() error {
	if !loginRe.MatchString(s.Username) {
		return fmt.Errorf("ftp: invalid username %q", s.Username)
	}
	if !loginRe.MatchString(s.SystemUser) {
		return fmt.Errorf("ftp: invalid system user %q", s.SystemUser)
	}
	if !path.IsAbs(s.HomeDir) || s.HomeDir != path.Clean(s.HomeDir) {
		return fmt.Errorf("ftp: home dir must be an absolute, clean path")
	}
	return nil
}

// Manager provisions and removes FTP virtual users. Idempotent.
type Manager interface {
	Provision(ctx context.Context, spec Spec) error
	Deprovision(ctx context.Context, username string) error
}

// GeneratePassword returns a strong random password safe to feed to pure-pw.
func GeneratePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ftp: generating password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
