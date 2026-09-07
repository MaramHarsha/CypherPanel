package updater

// Staging and the two-slot swap (agent-updates.md §3.4, §3.5).
//
// Everything is staged BESIDE the running binary, never in the state dir:
// rename(2) is atomic only within a filesystem, and /var/lib is usually a
// different mount from /usr/local/bin. The state directory is untouched by the
// swap, so identity and certificate survive by not being in its path at all.
//
// The write-temp / fsync / rename shape is not invented here — agent/identity
// already does exactly it for the certificate swap, and this follows that code.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Slot suffixes, beside the running binary.
const (
	newSuffix  = ".new"
	prevSuffix = ".prev"
)

func stagedPath(binary string) string {
	return filepath.Join(filepath.Dir(binary), "."+filepath.Base(binary)+newSuffix)
}

func previousPath(binary string) string {
	return filepath.Join(filepath.Dir(binary), "."+filepath.Base(binary)+prevSuffix)
}

// writeFileSynced writes and fsyncs before renaming into place. A power cut
// must not leave a zero-length file where a binary was.
func writeFileSynced(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// stage copies the artifact to the staged slot, hashing as it goes, and
// verifies the result against the signed digest before it is executable to
// anyone. It returns the staged path.
func stage(binary string, body io.Reader, want [32]byte) (string, error) {
	path := stagedPath(binary)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("updater: creating the staged binary: %w", err)
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("updater: downloading: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("updater: syncing the staged binary: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("updater: closing the staged binary: %w", err)
	}
	var got [32]byte
	copy(got[:], sum.Sum(nil))
	if got != want {
		_ = os.Remove(path)
		return "", ErrDigestMismatch
	}
	return path, nil
}

// preflight runs the staged file as a child: `<staged> version` must exit 0
// printing the version desired state named.
//
// One fork/exec, and it removes the whole class of artifacts that cannot run at
// all — wrong architecture, truncated download, an HTML error page served with
// a 200. That matters more than it looks: a binary that cannot reach main()
// cannot roll ITSELF back, so the only place to catch it is before the swap, in
// a process that still works.
func preflight(path, wantVersion string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput() //nolint:gosec // path is the file we just staged and verified
	if err != nil {
		return fmt.Errorf("updater: staged binary failed its pre-flight: %w", err)
	}
	printed := strings.TrimSpace(string(out))
	if !strings.Contains(printed, wantVersion) {
		return fmt.Errorf("updater: staged binary reports %q, desired state named %q", firstLine(printed), wantVersion)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// swap performs the two-slot rename: current → .prev, .new → current. Both are
// renames within one directory, so each is atomic and the pair leaves no window
// in which the binary is absent — only one in which .prev is stale, which
// nothing reads until a rollback.
func swap(binary, staged string) error {
	prev := previousPath(binary)
	if err := os.Remove(prev); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("updater: clearing the previous slot: %w", err)
	}
	if err := os.Link(binary, prev); err != nil {
		// A hard link keeps the running inode reachable under .prev without a
		// copy. Where links are unavailable (a bind-mounted single file), fall
		// back to a copy rather than giving up the rollback slot.
		if err := copyFile(binary, prev); err != nil {
			return fmt.Errorf("updater: keeping the previous binary: %w", err)
		}
	}
	if err := os.Rename(staged, binary); err != nil {
		return fmt.Errorf("updater: installing the new binary: %w", err)
	}
	return nil
}

// rollback puts the previous binary back. It takes no arguments it has to
// resolve and touches no network: it is the path that survives "cannot reach
// the control plane".
func rollback(binary string) error {
	prev := previousPath(binary)
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("updater: no previous binary to roll back to: %w", err)
	}
	if err := copyFile(prev, stagedPath(binary)); err != nil {
		return fmt.Errorf("updater: staging the previous binary: %w", err)
	}
	if err := os.Rename(stagedPath(binary), binary); err != nil {
		return fmt.Errorf("updater: restoring the previous binary: %w", err)
	}
	return nil
}

func copyFile(from, to string) error {
	src, err := os.Open(from) //nolint:gosec // both paths are agent-owned
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// writable reports whether the agent can actually replace its own binary. A
// host where it cannot — a package-managed install, an immutable image — is
// visibly EXCLUDED rather than failing forever (§9).
func writable(binary string) bool {
	f, err := os.OpenFile(binary, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
