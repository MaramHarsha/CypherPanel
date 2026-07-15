//go:build linux

package terminal

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"time"

	"github.com/creack/pty"
	"github.com/nats-io/nats.go"
)

var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// maxSession bounds a terminal session so orphaned PTYs can't linger forever.
const maxSession = time.Hour

// Serve subscribes to the control subject and spawns a PTY session per request.
func Serve(nc *nats.Conn, serverID string) (*nats.Subscription, error) {
	return nc.Subscribe(CtrlSubject(serverID), func(msg *nats.Msg) {
		var req StartRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil || !usernameRe.MatchString(req.Username) {
			return
		}
		go runSession(nc, req)
	})
}

// runSession opens a login shell AS THE ACCOUNT USER (su -) attached to a PTY,
// and bridges it to the session's NATS subjects.
func runSession(nc *nats.Conn, req StartRequest) {
	out := OutSubject(req.SessionID)
	publish := func(m Msg) {
		if b, err := json.Marshal(m); err == nil {
			_ = nc.Publish(out, b)
		}
	}

	// Account users have a locked (nologin) login shell for security, so we
	// force an interactive shell with `-s`. The user is still confined to its
	// own uid/gid; the agent is root so no password is needed.
	cmd := exec.Command("su", "-s", "/bin/bash", "-", req.Username)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		publish(Msg{Type: "data", Data: []byte("failed to start shell: " + err.Error() + "\r\n")})
		publish(Msg{Type: "close"})
		return
	}
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	if req.Cols > 0 && req.Rows > 0 {
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: req.Cols, Rows: req.Rows})
	}

	// browser → PTY
	sub, err := nc.Subscribe(InSubject(req.SessionID), func(m *nats.Msg) {
		var in Msg
		if json.Unmarshal(m.Data, &in) != nil {
			return
		}
		switch in.Type {
		case "data":
			_, _ = ptmx.Write(in.Data)
		case "resize":
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: in.Cols, Rows: in.Rows})
		case "close":
			_ = cmd.Process.Kill()
		}
	})
	if err != nil {
		return
	}
	defer sub.Unsubscribe()

	// Hard session cap.
	timer := time.AfterFunc(maxSession, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()

	// PTY → browser
	buf := make([]byte, 4096)
	for {
		n, rerr := ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			publish(Msg{Type: "data", Data: data})
		}
		if rerr != nil {
			break
		}
	}
	publish(Msg{Type: "close"})
}
