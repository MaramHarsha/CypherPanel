//go:build linux

package ftp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PureFTPd is the MVP-default Manager, driving Pure-FTPd's virtual-user database
// with pure-pw. It maps every virtual user onto the account's system user so
// the OS enforces file ownership/isolation.
type PureFTPd struct {
	// PureDBUID/GID: pure-pw needs a numeric uid/gid; we bind to the account's
	// system user by name via -u/-g, letting pure-pw resolve it.
}

func NewPureFTPd() *PureFTPd { return &PureFTPd{} }

func available() bool {
	_, err := exec.LookPath("pure-pw")
	return err == nil
}

// Provision creates (or updates) a virtual user. pure-pw reads the password
// twice from stdin; -m commits the change to the PureDB (no separate mkdb).
func (PureFTPd) Provision(ctx context.Context, spec Spec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	if spec.Password == "" {
		return fmt.Errorf("ftp: provision requires a password")
	}
	if !available() {
		return ErrUnsupported
	}
	// Delete-then-add makes create idempotent (pure-pw useradd errors if the
	// user exists). userdel of an absent user is harmless.
	_ = run(ctx, spec.Password, "userdel", spec.Username, "-m")
	if err := run(ctx, spec.Password, "useradd", spec.Username,
		"-u", spec.SystemUser, "-g", spec.SystemUser, "-d", spec.HomeDir, "-m"); err != nil {
		return err
	}
	return nil
}

// Deprovision removes a virtual user (idempotent).
func (PureFTPd) Deprovision(ctx context.Context, username string) error {
	if !loginRe.MatchString(username) {
		return fmt.Errorf("ftp: invalid username %q", username)
	}
	if !available() {
		return ErrUnsupported
	}
	return run(ctx, "", "userdel", username, "-m")
}

// run executes pure-pw, feeding the password twice on stdin when provided.
// Errors omit the argument vector's password (there is none — it goes via stdin).
func run(ctx context.Context, password string, args ...string) error {
	cmd := exec.CommandContext(ctx, "pure-pw", args...)
	if password != "" {
		cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ftp: pure-pw %s: %w: %s", args[0], err, out)
	}
	return nil
}
