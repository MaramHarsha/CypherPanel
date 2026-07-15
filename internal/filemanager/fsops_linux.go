//go:build linux

package filemanager

import (
	"archive/zip"
	"fmt"
	"io"
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
			if err := checkQuota(root, req.QuotaBytes, int64(len(req.Content))); err != nil {
				return err
			}
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
		case OpExtract:
			return extractZip(target, root, req.QuotaBytes)
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

// dirSize sums the on-disk size of a directory tree (used for quota checks).
func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// checkQuota rejects a write that would push the account over its disk limit.
func checkQuota(root string, quota, incoming int64) error {
	if quota <= 0 {
		return nil // unlimited
	}
	if dirSize(root)+incoming > quota {
		return fmt.Errorf("disk quota exceeded")
	}
	return nil
}

// Extraction limits guard against zip bombs.
const (
	maxExtractEntries = 10000
	maxExtractBytes   = 200 << 20 // 200 MiB expanded
)

// extractZip unpacks a zip archive into its own directory, validating EVERY
// entry's destination against the account root (zip-slip: an entry named
// `../../etc/passwd` is rejected before any write). Symlinks and oversized
// expansions are refused, and the account's disk quota is honoured.
func extractZip(archivePath, root string, quota int64) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("cannot open archive: %w", err)
	}
	defer zr.Close()

	if len(zr.File) > maxExtractEntries {
		return fmt.Errorf("archive has too many entries")
	}
	destDir := filepath.Dir(archivePath)

	used := dirSize(root)
	var expanded int64
	for _, f := range zr.File {
		// Reject entries that escape the root (zip-slip) or are symlinks.
		dst, err := safePathUnder(destDir, root, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive contains a symlink (%s) — refused", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		expanded += int64(f.UncompressedSize64)
		if expanded > maxExtractBytes {
			return fmt.Errorf("archive expands beyond the extraction limit")
		}
		if quota > 0 && used+expanded > quota {
			return fmt.Errorf("extraction would exceed the disk quota")
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, dst); err != nil {
			return err
		}
	}
	return nil
}

// safePathUnder joins base+rel and verifies the result stays under root
// (base is itself already under root).
func safePathUnder(base, root, rel string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, filepath.FromSlash(CleanRel(rel)))
	if !underRoot(rootReal, target) {
		return "", fmt.Errorf("archive entry %q escapes the account root", rel)
	}
	return target, nil
}

func writeZipEntry(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.CopyN(out, rc, maxExtractBytes)
	if err != nil && err != io.EOF {
		return err
	}
	return nil
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
