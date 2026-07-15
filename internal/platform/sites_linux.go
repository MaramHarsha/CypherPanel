//go:build linux

package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
)

type linuxSites struct{}

func newSites() Sites { return linuxSites{} }

func (linuxSites) Provision(ctx context.Context, spec SiteSpec) error {
	uid, gid, err := lookupIDs(spec.Username)
	if err != nil {
		return err
	}

	// Account-owned directories (web root, logs). Idempotent.
	for _, dir := range spec.AccountDirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("platform: mkdir %s: %w", dir, err)
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return fmt.Errorf("platform: chown %s: %w", dir, err)
		}
	}

	// PHP-FPM pool (root-owned system config).
	if err := writeRootConfig(spec.PoolPath, spec.PoolConfig); err != nil {
		return err
	}

	// Reload this account's PHP-FPM version so the new/updated pool is picked
	// up (best-effort: a box without that FPM installed still provisions).
	reloadPHPFPM(ctx, spec.PHPVersion)

	// Web-server vhost: write, then validate-then-reload with rollback so a
	// bad config never takes the web server down.
	if err := writeRootConfig(spec.VHostPath, spec.VHostConfig); err != nil {
		return err
	}
	if err := validateAndReloadNginx(ctx); err != nil {
		_ = os.Remove(spec.VHostPath) // roll back the offending vhost
		return err
	}
	return nil
}

func (linuxSites) RemovePHPPool(ctx context.Context, poolPath, phpVersion string) error {
	if err := os.Remove(poolPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: removing pool %s: %w", poolPath, err)
	}
	// Reload the old FPM so it drops the pool and releases the account socket
	// before the new version's pool claims it.
	reloadPHPFPM(ctx, phpVersion)
	return nil
}

func (linuxSites) Deprovision(ctx context.Context, vhostPath, poolPath string) error {
	// Remove is idempotent; missing files are fine.
	if err := os.Remove(vhostPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: removing %s: %w", vhostPath, err)
	}
	if err := os.Remove(poolPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: removing %s: %w", poolPath, err)
	}
	// Reload so nginx drops the removed vhost (best-effort; absence is fine).
	_ = validateAndReloadNginx(ctx)
	return nil
}

func (linuxSites) InstallCertificate(_ context.Context, certPath string, certPEM []byte, keyPath string, keyPEM []byte) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("platform: mkdir %s: %w", filepath.Dir(certPath), err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("platform: writing cert %s: %w", certPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return fmt.Errorf("platform: mkdir %s: %w", filepath.Dir(keyPath), err)
	}
	// Private key must never be world-readable.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("platform: writing key %s: %w", keyPath, err)
	}
	return nil
}

func (linuxSites) ApplyVHost(ctx context.Context, vhostPath string, config []byte) error {
	if err := writeRootConfig(vhostPath, config); err != nil {
		return err
	}
	if err := validateAndReloadNginx(ctx); err != nil {
		_ = os.Remove(vhostPath)
		return err
	}
	return nil
}

func lookupIDs(username string) (int, int, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return 0, 0, fmt.Errorf("platform: looking up user %s: %w", username, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}

func writeRootConfig(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("platform: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("platform: writing %s: %w", path, err)
	}
	return nil
}

// reloadPHPFPM reloads the given PHP branch's FPM service so pool changes take
// effect — reload, never restart (in-flight requests survive). Best-effort and
// version-independent: if systemd or that FPM service is absent (dev boxes,
// staging before PHP lands), it is a silent no-op, mirroring the nginx path.
// The Debian service name is php<version>-fpm (e.g. php8.3-fpm).
func reloadPHPFPM(ctx context.Context, phpVersion string) {
	if phpVersion == "" {
		return
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return // no systemd — nothing to reload here
	}
	service := "php" + phpVersion + "-fpm"
	// Only reload a service that exists and is active; ignore otherwise so a
	// version not installed on this box never fails the task.
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", service).Run(); err != nil {
		return
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "reload", service).CombinedOutput(); err != nil {
		// Log-worthy but non-fatal: the pool file is in place; an operator can
		// reload manually. Surfacing via error would fail an otherwise-applied
		// change and trigger pointless retries.
		slog.Warn("php-fpm reload failed", "service", service, "error", err, "output", string(out))
	}
}

// validateAndReloadNginx runs `nginx -t` then reloads. If nginx is not
// installed, monitoring will surface that; provisioning still succeeds so a
// box being staged before nginx lands is not blocked.
func validateAndReloadNginx(ctx context.Context) error {
	if _, err := exec.LookPath("nginx"); err != nil {
		return nil // nginx not installed yet — skip validate/reload
	}
	if out, err := exec.CommandContext(ctx, "nginx", "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("platform: nginx config validation failed: %w: %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "nginx", "-s", "reload").CombinedOutput(); err != nil {
		return fmt.Errorf("platform: nginx reload failed: %w: %s", err, out)
	}
	return nil
}
