package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Backup/restore transport (managed-databases.md §7). Data moves through the
// Docker archive API (docker cp), never through the exec stdout/stdin stream:
// the dump is written to a file inside the container and copied out as a tar
// (no multiplexed-stream framing to corrupt it), and a restore copies the dump
// file in and the restore command reads it by shell redirection (no exec stdin
// needed). ExecAndWait only runs a command to completion and reports its exit
// code.

// CopyFromContainer streams the tar archive of a path inside a container
// (GET /containers/{id}/archive). The caller extracts the file(s) from the tar
// and must Close the stream.
func (c *Client) CopyFromContainer(ctx context.Context, containerID, path string) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("path", path)
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+containerID+"/archive", q, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// CopyToContainer uploads a tar archive, extracting it under destDir inside the
// container (PUT /containers/{id}/archive). tar is a tar stream whose entries
// are written relative to destDir.
func (c *Client) CopyToContainer(ctx context.Context, containerID, destDir string, tar io.Reader) error {
	q := url.Values{}
	q.Set("path", destDir)
	resp, err := c.do(ctx, http.MethodPut, "/containers/"+containerID+"/archive", q, tar, "application/x-tar")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// ExecAndWait runs cmd inside a running container, waits for it to finish, and
// returns its exit code along with any captured output (stdout+stderr,
// demultiplexed) for diagnostics. A non-zero exit code is NOT an error — the
// caller decides what a non-zero code means for its operation.
func (c *Client) ExecAndWait(ctx context.Context, containerID string, cmd []string) (exitCode int, output []byte, err error) {
	execID, err := c.execCreate(ctx, containerID, cmd)
	if err != nil {
		return 0, nil, err
	}

	// Start (attached, non-detached) — the response is the hijacked, multiplexed
	// stdout/stderr stream. Drain it fully so the exec completes.
	startBody, _ := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	resp, err := c.do(ctx, http.MethodPost, "/exec/"+execID+"/start", nil, bytes.NewReader(startBody), "application/json")
	if err != nil {
		return 0, nil, fmt.Errorf("engine: exec start: %w", err)
	}
	out, derr := demuxDockerStream(resp.Body)
	_ = resp.Body.Close()
	if derr != nil {
		return 0, out, fmt.Errorf("engine: reading exec output: %w", derr)
	}

	code, err := c.execExitCode(ctx, execID)
	if err != nil {
		return 0, out, err
	}
	return code, out, nil
}

func (c *Client) execCreate(ctx context.Context, containerID string, cmd []string) (string, error) {
	cfg := map[string]any{"AttachStdout": true, "AttachStderr": true, "Cmd": cmd}
	var resp struct {
		ID string `json:"Id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/"+containerID+"/exec", nil, cfg, &resp); err != nil {
		return "", fmt.Errorf("engine: exec create: %w", err)
	}
	return resp.ID, nil
}

func (c *Client) execExitCode(ctx context.Context, execID string) (int, error) {
	var info struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/exec/"+execID+"/json", nil, nil, &info); err != nil {
		return 0, fmt.Errorf("engine: exec inspect: %w", err)
	}
	if info.Running {
		// The start call drains to completion before we inspect, so this is
		// effectively never hit; treat a still-running exec as a failure rather
		// than reporting a bogus zero code.
		return 0, fmt.Errorf("engine: exec still running after output drained")
	}
	return info.ExitCode, nil
}

// demuxDockerStream reads Docker's multiplexed exec/attach stream (8-byte frame
// header [stream, 0,0,0, size(4 BE)] + payload) and returns the concatenated
// stdout+stderr payloads. This is the framing that, left in place, corrupted
// the first cut's dumps — here it is stripped, and the dump data itself never
// flows through this path (it goes through the archive API).
func demuxDockerStream(r io.Reader) ([]byte, error) {
	var out bytes.Buffer
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out.Bytes(), nil
			}
			return out.Bytes(), err
		}
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}
		if _, err := io.CopyN(&out, r, int64(size)); err != nil {
			return out.Bytes(), err
		}
	}
}
