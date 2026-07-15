// Package filemanager defines the Core↔Agent file-operation protocol and the
// path-safety primitives shared by both sides. Actual filesystem access happens
// only on the agent (as the account's uid/gid); Core reaches it via NATS
// request-reply. This is the highest-risk surface in the product — every path
// is treated as hostile (see the filesystem-operations-safety skill).
package filemanager

import (
	"path"
	"strings"
)

// Op is a file-manager operation.
type Op string

const (
	OpList    Op = "list"
	OpRead    Op = "read"
	OpWrite   Op = "write"
	OpMkdir   Op = "mkdir"
	OpDelete  Op = "delete"
	OpRename  Op = "rename"
	OpExtract Op = "extract" // unpack a zip archive already in the account tree
)

// Request is a single file operation, addressed to one hosting account. Path
// and NewPath are relative to the account root; the agent resolves them.
type Request struct {
	Op         Op     `json:"op"`
	Username   string `json:"username"` // account system user (root + uid/gid owner)
	Path       string `json:"path"`
	NewPath    string `json:"new_path,omitempty"` // rename destination
	Content    []byte `json:"content,omitempty"`  // write body
	QuotaBytes int64  `json:"quota_bytes,omitempty"` // account disk limit (0 = unlimited)
}

// Entry is one directory listing item.
type Entry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"` // RFC3339
}

// Response carries an operation's result. Error (non-empty) means it failed.
type Response struct {
	Error   string  `json:"error,omitempty"`
	Entries []Entry `json:"entries,omitempty"`
	Content []byte  `json:"content,omitempty"`
}

// Subject is the NATS request subject a server's agent listens on.
func Subject(serverID string) string { return "fs.server." + serverID }

// CleanRel neutralises path traversal: it treats rel as rooted, cleans it (so
// any `..` that would escape is collapsed), and returns a forward-slash path
// that can NEVER escape the account root when joined to it. Backslashes are
// normalised first so a Windows-style input can't smuggle a separator through.
// This is the pure, host-OS-independent half of path safety; the agent still
// performs a symlink-resolved under-root re-check before touching the FS.
func CleanRel(rel string) string {
	rel = strings.ReplaceAll(rel, "\\", "/")
	cleaned := path.Clean("/" + rel) // e.g. "/../../etc" -> "/etc"
	return strings.TrimPrefix(cleaned, "/")
}
