//go:build linux

package filemanager

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Size caps for the text-oriented MVP file manager.
const (
	maxReadBytes  = 2 << 20 // 2 MiB
	maxWriteBytes = 2 << 20
)

// Handler executes file operations inside an account's home directory, as the
// account's uid/gid (so the OS enforces isolation even if an app-level check is
// missed). HomeRoot maps a system user to its home (injected from the distro
// path layer, never hardcoded).
type Handler struct {
	HomeRoot func(username string) string
}

// Handle runs one request and returns a Response (never panics; all failures
// are returned as Response.Error).
func (h *Handler) Handle(req Request) Response {
	root := h.HomeRoot(req.Username)
	uid, gid, err := lookupIDs(req.Username)
	if err != nil {
		return Response{Error: err.Error()}
	}

	target, err := safePath(root, req.Path)
	if err != nil {
		return Response{Error: err.Error()}
	}

	var resp Response
	opErr := h.asUser(uid, gid, func() error {
		switch req.Op {
		case OpList:
			entries, err := listDir(target)
			resp.Entries = entries
			return err
		case OpRead:
			content, err := readFile(target)
			resp.Content = content
			return err
		case OpWrite:
			return writeFile(target, req.Content)
		case OpMkdir:
			return os.Mkdir(target, 0o755)
		case OpDelete:
			return os.RemoveAll(target)
		case OpRename:
			dst, err := safePath(root, req.NewPath)
			if err != nil {
				return err
			}
			return os.Rename(target, dst)
		default:
			return fmt.Errorf("unknown op %q", req.Op)
		}
	})
	if opErr != nil {
		resp.Error = opErr.Error()
	}
	return resp
}

// safePath joins rel under root and re-verifies — after resolving symlinks on
// the deepest existing ancestor — that the result is still inside root. This
// defeats both `..` traversal (CleanRel) and symlink escapes.
func safePath(root, rel string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("account root unavailable: %w", err)
	}
	target := filepath.Join(rootReal, filepath.FromSlash(CleanRel(rel)))

	// Resolve symlinks on the longest existing prefix of target, then confirm
	// containment. (A not-yet-existing leaf like a new file/dir is fine.)
	probe := target
	for {
		if resolved, err := filepath.EvalSymlinks(probe); err == nil {
			if !underRoot(rootReal, resolved) {
				return "", fmt.Errorf("path escapes account root")
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("cannot resolve path")
		}
		probe = parent
	}
	if !underRoot(rootReal, target) {
		return "", fmt.Errorf("path escapes account root")
	}
	return target, nil
}

func underRoot(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// asUser runs fn with the calling thread's filesystem uid/gid set to the
// account user, then restores root. The goroutine is pinned to its OS thread so
// the credential change is isolated to this operation.
func (h *Handler) asUser(uid, gid int, fn func() error) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if serr := unix.Setfsgid(gid); serr != nil {
		return fmt.Errorf("dropping gid: %w", serr)
	}
	if serr := unix.Setfsuid(uid); serr != nil {
		_ = unix.Setfsgid(0)
		return fmt.Errorf("dropping uid: %w", serr)
	}
	// Restore uid before gid (reverse of the drop order).
	defer func() { _ = unix.Setfsgid(0) }()
	defer func() { _ = unix.Setfsuid(0) }()

	return fn()
}

func listDir(dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    it.Name(),
			IsDir:   it.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	// Directories first, then name — stable, predictable UI ordering.
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func readFile(p string) ([]byte, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("is a directory")
	}
	if info.Size() > maxReadBytes {
		return nil, fmt.Errorf("file too large to open in the editor (%d bytes)", info.Size())
	}
	return os.ReadFile(p)
}

func writeFile(p string, content []byte) error {
	if len(content) > maxWriteBytes {
		return fmt.Errorf("content exceeds the %d-byte limit", maxWriteBytes)
	}
	return os.WriteFile(p, content, 0o644)
}

func lookupIDs(username string) (int, int, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return 0, 0, fmt.Errorf("looking up account user: %w", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid == 0 || gid == 0 {
		return 0, 0, fmt.Errorf("refusing to operate as uid/gid 0")
	}
	return uid, gid, nil
}
