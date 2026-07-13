//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
)

type linuxSystemUsers struct{}

func newSystemUsers() SystemUsers {
	return linuxSystemUsers{}
}

func (linuxSystemUsers) Create(ctx context.Context, username, homeDir string) error {
	if exists, err := (linuxSystemUsers{}).Exists(ctx, username); err != nil {
		return err
	} else if exists {
		return nil
	}
	// Locked account: hosted users authenticate through the panel/FTP/SSH
	// keys, never a system password.
	out, err := exec.CommandContext(ctx, "useradd",
		"--create-home", "--home-dir", homeDir, "--shell", "/usr/sbin/nologin", username,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("platform: useradd %s: %w: %s", username, err, out)
	}
	return nil
}

func (linuxSystemUsers) Remove(ctx context.Context, username string) error {
	if exists, err := (linuxSystemUsers{}).Exists(ctx, username); err != nil {
		return err
	} else if !exists {
		return nil
	}
	out, err := exec.CommandContext(ctx, "userdel", "--remove", username).CombinedOutput()
	if err != nil {
		return fmt.Errorf("platform: userdel %s: %w: %s", username, err, out)
	}
	return nil
}

func (linuxSystemUsers) Exists(_ context.Context, username string) (bool, error) {
	_, err := user.Lookup(username)
	if err == nil {
		return true, nil
	}
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return false, nil
	}
	return false, fmt.Errorf("platform: looking up user %s: %w", username, err)
}
